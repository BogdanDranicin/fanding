// Package journal records a daily audit row for every CBR rate publication:
// the exact detection time, the published rates, the funding computed from them,
// the live forecast made just before publication, and the race-winner diagnostics.
// It is the durable source of truth behind the /api/v1/cb-publications journal page.
package journal

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/source/cbr"
	"github.com/funding-service/backend/internal/storage"
)

var msk = time.FixedZone("MSK", 3*60*60)

const dayFmt = "2006-01-02"

// pollInterval controls how often the recorder samples the live snapshot to
// capture the pre-publication forecast and to re-fix funding after publication.
const pollInterval = time.Minute

// Store is the storage surface the Recorder needs; *storage.Store satisfies it.
// Narrowed to an interface so the recorder is testable without a database.
type Store interface {
	UpsertCBPublication(ctx context.Context, in storage.CBPublicationInput) error
	RecentCBPublications(ctx context.Context, days int) ([]storage.CBPublicationRow, error)
}

// Recorder persists CBR-publication audit rows. It does not own any of its
// dependencies; the caller wires the store, the engine snapshot function and the
// CBR source's publication-info accessor.
type Recorder struct {
	store      Store
	snapshotFn func() funding.FundingSnapshot
	pubInfoFn  func() cbr.PublicationInfo
	log        zerolog.Logger
	now        func() time.Time // injectable clock for tests

	lastPubDay string // MSK "2006-01-02" of the last publication we recorded
	lastFP     string // fingerprint of the last successfully written poll values
}

// New creates a Recorder.
func New(
	store Store,
	snapshotFn func() funding.FundingSnapshot,
	pubInfoFn func() cbr.PublicationInfo,
	log zerolog.Logger,
) *Recorder {
	return &Recorder{
		store:      store,
		snapshotFn: snapshotFn,
		pubInfoFn:  pubInfoFn,
		log:        log.With().Str("component", "journal").Logger(),
		now:        time.Now,
	}
}

// Run blocks until ctx is cancelled. It stamps a row on each publication signal
// and, on a poll ticker, keeps that row's forecast and funding fresh.
func (r *Recorder) Run(ctx context.Context, pubCh <-chan time.Time) {
	r.hydrate(ctx)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-pubCh:
			if !ok {
				return
			}
			r.recordPublication(ctx, t)
		case <-ticker.C:
			r.recordPoll(ctx)
		}
	}
}

// hydrate restores lastPubDay from the DB so the evening re-fix keeps running
// after a restart that happens between publication (~16:30) and MSK midnight.
func (r *Recorder) hydrate(ctx context.Context) {
	rows, err := r.store.RecentCBPublications(ctx, 1)
	if err != nil {
		r.log.Warn().Err(err).Msg("journal hydrate failed")
		return
	}
	today := dayStart(r.now()).Format(dayFmt)
	for _, row := range rows {
		if row.DetectedAt != nil && row.Date.Format(dayFmt) == today {
			r.lastPubDay = today
		}
	}
}

// recordPublication stamps the exact publication moment: detection time, the
// rates as fetched from the winning CBR channel, the funding reconstructed so
// far, and the race-winner diagnostics.
func (r *Recorder) recordPublication(ctx context.Context, t time.Time) {
	snap := r.snapshotFn()
	info := r.pubInfoFn()
	day := dayStart(t)
	r.lastPubDay = day.Format(dayFmt)

	in := storage.CBPublicationInput{
		Date:       day,
		DetectedAt: &t,
		UpdatedAt:  r.now(),
		// Rates come from the CBR fetch itself, not the engine snapshot: the
		// KindNewOfficialRate ticks reach the engine asynchronously, so at signal
		// time the snapshot may still hold yesterday's rate.
		USDRate:      posPtr(info.USD),
		EURRate:      posPtr(info.EUR),
		CNYRate:      posPtr(info.CNY),
		CBFundingUSD: snap.USDRUBF.CBFunding,
		CBFundingEUR: snap.EURRUBF.CBFunding,
		CNYFunding:   snap.CNYRUBF.MOEXFunding,

		SettlVWAPUSD:           snap.USDRUBF.SettlVWAP,
		SettlVWAPEUR:           snap.EURRUBF.SettlVWAP,
		MOEXFundingUSD:         snap.USDRUBF.MOEXFunding,
		MOEXFundingEUR:         snap.EURRUBF.MOEXFunding,
		CBFundingNoDeadbandUSD: snap.USDRUBF.CBFundingNoDeadband,
		CBFundingNoDeadbandEUR: snap.EURRUBF.CBFundingNoDeadband,
	}
	if info.Winner != "" {
		in.WinnerChannel = &info.Winner
	}
	if info.LatencyMs > 0 {
		in.WinnerLatencyMs = &info.LatencyMs
	}

	if err := r.store.UpsertCBPublication(ctx, in); err != nil {
		r.log.Warn().Err(err).Msg("record publication failed")
		return
	}
	r.log.Warn().
		Str("rate_date", info.Date).
		Str("winner", info.Winner).
		Int64("winner_latency_ms", info.LatencyMs).
		Str("msk_time", t.In(msk).Format("15:04:05")).
		Msg("cbr publication journaled")
}

// recordPoll captures the live forecast before publication and re-fixes funding
// after it, so the row converges on the final MOEX SWAPRATE published at evening
// clearing. Writes are deduplicated by value: when the engine state is frozen
// (nights, holidays, the post-15:30 forecast plateau) the fingerprint is stable
// and no DB write happens, so idle periods produce no rows and no churn.
func (r *Recorder) recordPoll(ctx context.Context) {
	now := r.now().In(msk)
	// MOEX and CBR are idle on weekends: never create weekend rows, even from
	// stale engine state left over from Friday.
	if wd := now.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return
	}

	snap := r.snapshotFn()
	day := dayStart(now)

	// Nil fields leave stored values untouched (COALESCE in the upsert), so
	// unconditional assignment is safe here.
	in := storage.CBPublicationInput{
		Date:                day,
		UpdatedAt:           r.now(),
		PredictedFundingUSD: snap.USDRUBF.PredictedFunding,
		PredictedFundingEUR: snap.EURRUBF.PredictedFunding,
		PredictedCBRateUSD:  snap.USDRUBF.PredictedCBRate,
		PredictedCBRateEUR:  snap.EURRUBF.PredictedCBRate,
	}

	// Post-publication re-fix: only for the day we recorded a publication, so we
	// never touch a past day's funding and never create a row without cause.
	// Через вечер сюда же подтягивается фактический MOEX SWAPRATE (moex_funding),
	// чтобы строка сошлась на биржевой факт и стала видна сверка с реконструкцией.
	if r.lastPubDay == day.Format(dayFmt) {
		in.CBFundingUSD = snap.USDRUBF.CBFunding
		in.CBFundingEUR = snap.EURRUBF.CBFunding
		in.CNYFunding = snap.CNYRUBF.MOEXFunding

		in.SettlVWAPUSD = snap.USDRUBF.SettlVWAP
		in.SettlVWAPEUR = snap.EURRUBF.SettlVWAP
		in.MOEXFundingUSD = snap.USDRUBF.MOEXFunding
		in.MOEXFundingEUR = snap.EURRUBF.MOEXFunding
		in.CBFundingNoDeadbandUSD = snap.USDRUBF.CBFundingNoDeadband
		in.CBFundingNoDeadbandEUR = snap.EURRUBF.CBFundingNoDeadband
	}

	fp := fingerprint(&in)
	if fp == emptyFP || fp == r.lastFP {
		return
	}
	if err := r.store.UpsertCBPublication(ctx, in); err != nil {
		r.log.Warn().Err(err).Msg("record poll failed")
		return
	}
	r.lastFP = fp
}

// emptyFP is fingerprint() of an input whose value fields are all nil.
const emptyFP = "-|-|-|-|-|-|-|-|-|-|-|-|-"

// fingerprint serializes the value fields recordPoll writes, so identical
// consecutive polls can be skipped. Date/UpdatedAt are deliberately excluded:
// frozen values carried over a midnight or holiday rollover must NOT trigger a
// write for the new day.
func fingerprint(in *storage.CBPublicationInput) string {
	f := func(p *float64) string {
		if p == nil {
			return "-"
		}
		return strconv.FormatFloat(*p, 'g', -1, 64)
	}
	return strings.Join([]string{
		f(in.PredictedFundingUSD), f(in.PredictedFundingEUR),
		f(in.PredictedCBRateUSD), f(in.PredictedCBRateEUR),
		f(in.CBFundingUSD), f(in.CBFundingEUR), f(in.CNYFunding),
		f(in.SettlVWAPUSD), f(in.SettlVWAPEUR),
		f(in.MOEXFundingUSD), f(in.MOEXFundingEUR),
		f(in.CBFundingNoDeadbandUSD), f(in.CBFundingNoDeadbandEUR),
	}, "|")
}

// posPtr returns a pointer to v when v is a plausible rate (> 0), else nil.
func posPtr(v float64) *float64 {
	if v <= 0 {
		return nil
	}
	return &v
}

// dayStart returns midnight of t's MSK calendar day, as a UTC-anchored DATE value.
func dayStart(t time.Time) time.Time {
	m := t.In(msk)
	return time.Date(m.Year(), m.Month(), m.Day(), 0, 0, 0, 0, time.UTC)
}
