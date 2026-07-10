package journal

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/source/cbr"
	"github.com/funding-service/backend/internal/storage"
)

func TestDayStart(t *testing.T) {
	cases := []struct {
		name string
		in   time.Time
		want string // expected YYYY-MM-DD of the MSK calendar day
	}{
		{
			name: "morning MSK stays same day",
			in:   time.Date(2026, 7, 10, 5, 0, 0, 0, time.UTC), // 08:00 MSK
			want: "2026-07-10",
		},
		{
			name: "just after MSK midnight rolls to next day",
			in:   time.Date(2026, 7, 10, 21, 30, 0, 0, time.UTC), // 00:30 MSK, 11 Jul
			want: "2026-07-11",
		},
		{
			name: "just before MSK midnight stays same day",
			in:   time.Date(2026, 7, 10, 20, 30, 0, 0, time.UTC), // 23:30 MSK, 10 Jul
			want: "2026-07-10",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := dayStart(c.in)
			if got.Format(dayFmt) != c.want {
				t.Errorf("dayStart(%s) = %s, want %s", c.in.Format(time.RFC3339), got.Format(dayFmt), c.want)
			}
			if h, m, s := got.Clock(); h != 0 || m != 0 || s != 0 {
				t.Errorf("dayStart not at midnight: %v", got)
			}
			if got.Location() != time.UTC {
				t.Errorf("dayStart location = %v, want UTC", got.Location())
			}
		})
	}
}

// fakeStore records every upsert and serves canned rows for hydrate.
type fakeStore struct {
	upserts []storage.CBPublicationInput
	rows    []storage.CBPublicationRow
	err     error
}

func (f *fakeStore) UpsertCBPublication(_ context.Context, in storage.CBPublicationInput) error {
	if f.err != nil {
		return f.err
	}
	f.upserts = append(f.upserts, in)
	return nil
}

func (f *fakeStore) RecentCBPublications(_ context.Context, _ int) ([]storage.CBPublicationRow, error) {
	return f.rows, f.err
}

func fptr(v float64) *float64 { return &v }

// newTestRecorder wires a Recorder around fakes. at is the frozen clock value.
func newTestRecorder(fs *fakeStore, snap funding.FundingSnapshot, info cbr.PublicationInfo, at time.Time) *Recorder {
	r := New(fs,
		func() funding.FundingSnapshot { return snap },
		func() cbr.PublicationInfo { return info },
		zerolog.Nop())
	r.now = func() time.Time { return at }
	return r
}

// Tuesday 2026-07-07 16:45 MSK (13:45 UTC) — a plausible publication moment.
var pubMoment = time.Date(2026, 7, 7, 13, 45, 0, 0, time.UTC)

func TestRecordPublicationUsesChannelRates(t *testing.T) {
	fs := &fakeStore{}
	// Engine snapshot still holds yesterday's rate (the race this design avoids).
	snap := funding.FundingSnapshot{}
	snap.USDRUBF.OfficialRate = fptr(75.93)
	snap.USDRUBF.CBFunding = fptr(0.11696)
	info := cbr.PublicationInfo{Date: "08.07.2026", USD: 76.41, EUR: 87.02, CNY: 11.21, Winner: "cbr_soap", LatencyMs: 94}

	r := newTestRecorder(fs, snap, info, pubMoment)
	r.recordPublication(context.Background(), pubMoment)

	if len(fs.upserts) != 1 {
		t.Fatalf("want 1 upsert, got %d", len(fs.upserts))
	}
	in := fs.upserts[0]
	if in.USDRate == nil || *in.USDRate != 76.41 {
		t.Errorf("USDRate = %v, want 76.41 (from channel fetch, not stale engine 75.93)", in.USDRate)
	}
	if in.CNYRate == nil || *in.CNYRate != 11.21 {
		t.Errorf("CNYRate = %v, want 11.21", in.CNYRate)
	}
	if in.DetectedAt == nil || !in.DetectedAt.Equal(pubMoment) {
		t.Errorf("DetectedAt = %v, want %v", in.DetectedAt, pubMoment)
	}
	if in.WinnerChannel == nil || *in.WinnerChannel != "cbr_soap" {
		t.Errorf("WinnerChannel = %v, want cbr_soap", in.WinnerChannel)
	}
	if in.CBFundingUSD == nil || *in.CBFundingUSD != 0.11696 {
		t.Errorf("CBFundingUSD = %v, want 0.11696 (funding still comes from the snapshot)", in.CBFundingUSD)
	}
	if r.lastPubDay != "2026-07-07" {
		t.Errorf("lastPubDay = %q, want 2026-07-07", r.lastPubDay)
	}
}

func TestRecordPollDedupsIdenticalValues(t *testing.T) {
	fs := &fakeStore{}
	snap := funding.FundingSnapshot{}
	snap.USDRUBF.PredictedCBRate = fptr(76.10)
	r := newTestRecorder(fs, snap, cbr.PublicationInfo{}, pubMoment)

	r.recordPoll(context.Background())
	r.recordPoll(context.Background()) // identical snapshot → no second write
	if len(fs.upserts) != 1 {
		t.Fatalf("want 1 upsert after two identical polls, got %d", len(fs.upserts))
	}

	// A changed forecast writes again.
	snap.USDRUBF.PredictedCBRate = fptr(76.15)
	r.snapshotFn = func() funding.FundingSnapshot { return snap }
	r.recordPoll(context.Background())
	if len(fs.upserts) != 2 {
		t.Fatalf("want 2 upserts after forecast change, got %d", len(fs.upserts))
	}
}

func TestRecordPollSkipsWeekendAndEmpty(t *testing.T) {
	fs := &fakeStore{}
	snap := funding.FundingSnapshot{}
	snap.USDRUBF.PredictedCBRate = fptr(76.10) // stale Friday leftovers

	saturday := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC) // Sat 12:00 MSK
	r := newTestRecorder(fs, snap, cbr.PublicationInfo{}, saturday)
	r.recordPoll(context.Background())
	if len(fs.upserts) != 0 {
		t.Fatalf("weekend poll must not write, got %d upserts", len(fs.upserts))
	}

	// A weekday poll with a fully empty snapshot must not create a row either.
	r2 := newTestRecorder(fs, funding.FundingSnapshot{}, cbr.PublicationInfo{}, pubMoment)
	r2.recordPoll(context.Background())
	if len(fs.upserts) != 0 {
		t.Fatalf("empty snapshot poll must not write, got %d upserts", len(fs.upserts))
	}
}

func TestRecordPollRefixOnlyAfterPublication(t *testing.T) {
	fs := &fakeStore{}
	snap := funding.FundingSnapshot{}
	snap.USDRUBF.PredictedCBRate = fptr(76.10)
	snap.USDRUBF.CBFunding = fptr(0.117)

	r := newTestRecorder(fs, snap, cbr.PublicationInfo{}, pubMoment)
	r.recordPoll(context.Background())
	if len(fs.upserts) != 1 {
		t.Fatalf("want 1 upsert, got %d", len(fs.upserts))
	}
	if fs.upserts[0].CBFundingUSD != nil {
		t.Errorf("CBFunding must not be written before a publication is recorded")
	}

	// After a publication the same poll values gain the funding re-fix.
	r.lastPubDay = "2026-07-07"
	r.recordPoll(context.Background())
	if len(fs.upserts) != 2 {
		t.Fatalf("want 2 upserts (fingerprint changed by re-fix fields), got %d", len(fs.upserts))
	}
	if fs.upserts[1].CBFundingUSD == nil || *fs.upserts[1].CBFundingUSD != 0.117 {
		t.Errorf("CBFundingUSD = %v, want 0.117 after publication", fs.upserts[1].CBFundingUSD)
	}
}

func TestHydrateRestoresLastPubDay(t *testing.T) {
	detected := pubMoment
	fs := &fakeStore{rows: []storage.CBPublicationRow{
		{Date: time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC), DetectedAt: &detected},
	}}
	r := newTestRecorder(fs, funding.FundingSnapshot{}, cbr.PublicationInfo{}, pubMoment)
	r.hydrate(context.Background())
	if r.lastPubDay != "2026-07-07" {
		t.Errorf("lastPubDay = %q, want 2026-07-07 (restored from DB)", r.lastPubDay)
	}

	// A row without DetectedAt (forecast-only) must not arm the re-fix.
	fs2 := &fakeStore{rows: []storage.CBPublicationRow{
		{Date: time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)},
	}}
	r2 := newTestRecorder(fs2, funding.FundingSnapshot{}, cbr.PublicationInfo{}, pubMoment)
	r2.hydrate(context.Background())
	if r2.lastPubDay != "" {
		t.Errorf("lastPubDay = %q, want empty for forecast-only row", r2.lastPubDay)
	}
}
