// Package journal records a daily audit row for every CBR rate publication:
// the exact detection time, the published rates, the funding computed from them,
// the live forecast made just before publication, and the race-winner diagnostics.
// It is the durable source of truth behind the /api/v1/cb-publications journal page.
package journal

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/storage"
)

var msk = time.FixedZone("MSK", 3*60*60)

// pollInterval controls how often the recorder samples the live snapshot to
// capture the pre-publication forecast and to re-fix funding after publication.
const pollInterval = time.Minute

// Recorder persists CBR-publication audit rows. It does not own any of its
// dependencies; the caller wires the store, the engine snapshot function and the
// CBR source's publication-info accessor.
type Recorder struct {
	store      *storage.Store
	snapshotFn func() funding.FundingSnapshot
	pubInfoFn  func() (date, winner string, latencyMs int64)
	log        zerolog.Logger

	lastPubDay string // MSK "2006-01-02" of the last publication we recorded
}

// New creates a Recorder.
func New(
	store *storage.Store,
	snapshotFn func() funding.FundingSnapshot,
	pubInfoFn func() (string, string, int64),
	log zerolog.Logger,
) *Recorder {
	return &Recorder{store: store, snapshotFn: snapshotFn, pubInfoFn: pubInfoFn, log: log.With().Str("component", "journal").Logger()}
}

// Run blocks until ctx is cancelled. It stamps a row on each publication signal
// and, on a poll ticker, keeps that row's forecast and funding fresh.
func (r *Recorder) Run(ctx context.Context, pubCh <-chan time.Time) {
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

// recordPublication stamps the exact publication moment: detection time, published
// rates, reconstructed funding and the race-winner diagnostics.
func (r *Recorder) recordPublication(ctx context.Context, t time.Time) {
	snap := r.snapshotFn()
	rateDate, winner, latencyMs := r.pubInfoFn()
	day := dayStart(t)
	r.lastPubDay = day.Format("2006-01-02")

	in := storage.CBPublicationInput{
		Date:         day,
		DetectedAt:   &t,
		UpdatedAt:    time.Now(),
		USDRate:      snap.USDRUBF.OfficialRate,
		EURRate:      snap.EURRUBF.OfficialRate,
		CBFundingUSD: snap.USDRUBF.CBFunding,
		CBFundingEUR: snap.EURRUBF.CBFunding,
		CNYFunding:   snap.CNYRUBF.MOEXFunding,
	}
	if winner != "" {
		in.WinnerChannel = &winner
	}
	if latencyMs > 0 {
		in.WinnerLatencyMs = &latencyMs
	}

	if err := r.store.UpsertCBPublication(ctx, in); err != nil {
		r.log.Warn().Err(err).Msg("record publication failed")
		return
	}
	r.log.Warn().
		Str("rate_date", rateDate).
		Str("winner", winner).
		Int64("winner_latency_ms", latencyMs).
		Str("msk_time", t.In(msk).Format("15:04:05")).
		Msg("cbr publication journaled")
}

// recordPoll captures the live forecast before publication and re-fixes funding
// after it, so the row converges on the final MOEX SWAPRATE. It writes nothing on
// idle days: a row is created only once there is a forecast or a same-day publication.
func (r *Recorder) recordPoll(ctx context.Context) {
	snap := r.snapshotFn()
	now := time.Now()
	day := dayStart(now.In(msk))
	today := day.Format("2006-01-02")

	in := storage.CBPublicationInput{Date: day, UpdatedAt: now}
	write := false

	// Pre-publication forecast: overwrite with the newest non-nil so the stored
	// value settles on the last forecast shown before the rate was published.
	if p := snap.USDRUBF.PredictedFunding; p != nil {
		in.PredictedFundingUSD = p
		write = true
	}
	if p := snap.EURRUBF.PredictedFunding; p != nil {
		in.PredictedFundingEUR = p
		write = true
	}
	if p := snap.USDRUBF.PredictedCBRate; p != nil {
		in.PredictedCBRateUSD = p
		write = true
	}
	if p := snap.EURRUBF.PredictedCBRate; p != nil {
		in.PredictedCBRateEUR = p
		write = true
	}

	// Post-publication re-fix: only for the day we already recorded a publication,
	// so we never create an empty row and never touch a past day's funding.
	if r.lastPubDay == today {
		in.CBFundingUSD = snap.USDRUBF.CBFunding
		in.CBFundingEUR = snap.EURRUBF.CBFunding
		in.CNYFunding = snap.CNYRUBF.MOEXFunding
		write = true
	}

	if !write {
		return
	}
	if err := r.store.UpsertCBPublication(ctx, in); err != nil {
		r.log.Warn().Err(err).Msg("record poll failed")
	}
}

// dayStart returns midnight of t's MSK calendar day, as a UTC-anchored DATE value.
func dayStart(t time.Time) time.Time {
	m := t.In(msk)
	return time.Date(m.Year(), m.Month(), m.Day(), 0, 0, 0, 0, time.UTC)
}
