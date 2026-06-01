package storage

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/source"
)

const (
	tickBatchSize  = 500
	tickFlushEvery = time.Second
	snapWriteEvery = 5 * time.Second
)

// Writer asynchronously persists ticks and funding snapshots to the database.
// It never blocks the caller: ticks are buffered, snapshots are sampled every 5 s.
type Writer struct {
	store  *Store
	tickCh <-chan source.Tick
	snapCh <-chan funding.FundingSnapshot
	log    zerolog.Logger
}

// NewWriter creates a Writer. tickCh and snapCh must already be wired to their producers.
func NewWriter(
	store *Store,
	tickCh <-chan source.Tick,
	snapCh <-chan funding.FundingSnapshot,
	log zerolog.Logger,
) *Writer {
	return &Writer{store: store, tickCh: tickCh, snapCh: snapCh, log: log}
}

// Run blocks until ctx is cancelled, running tick batching and snapshot persistence in parallel.
func (w *Writer) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); w.runTicks(ctx) }()
	go func() { defer wg.Done(); w.runSnaps(ctx) }()
	wg.Wait()
}

// runTicks reads from tickCh, batches by count (500) or time (1 s), then bulk-inserts.
func (w *Writer) runTicks(ctx context.Context) {
	buf := make([]source.Tick, 0, tickBatchSize)
	flushTimer := time.NewTicker(tickFlushEvery)
	defer flushTimer.Stop()

	flush := func() {
		if len(buf) == 0 {
			return
		}
		if err := w.store.BatchInsertTicks(ctx, buf); err != nil {
			w.log.Warn().Err(err).Int("count", len(buf)).Msg("tick batch insert failed")
		}
		buf = buf[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case tick, ok := <-w.tickCh:
			if !ok {
				flush()
				return
			}
			buf = append(buf, tick)
			if len(buf) >= tickBatchSize {
				flush()
			}
		case <-flushTimer.C:
			flush()
		}
	}
}

// runSnaps reads every snapshot (logging each one), and writes the latest to the DB every 5 s.
func (w *Writer) runSnaps(ctx context.Context) {
	writeTimer := time.NewTicker(snapWriteEvery)
	defer writeTimer.Stop()

	var latest *funding.FundingSnapshot

	for {
		select {
		case <-ctx.Done():
			return
		case snap, ok := <-w.snapCh:
			if !ok {
				return
			}
			ev := w.log.Info().
				Float64("usdrubf_vwap", snap.USDRUBF.VWAP).
				Float64("eurrubf_vwap", snap.EURRUBF.VWAP).
				Float64("cnyrubf_vwap", snap.CNYRUBF.VWAP)
			if snap.USDRUBF.OfficialRate != nil {
				ev = ev.Float64("usd_official", *snap.USDRUBF.OfficialRate)
			}
			if snap.USDRUBF.CBFunding != nil {
				ev = ev.Float64("usd_cb_funding", *snap.USDRUBF.CBFunding)
			}
			if snap.USDRUBF.PredictedFunding != nil {
				ev = ev.Float64("usd_predicted", *snap.USDRUBF.PredictedFunding)
			}
			if snap.USDRUBF.PredictedCBRate != nil {
				ev = ev.Float64("usd_predicted_cb_rate", *snap.USDRUBF.PredictedCBRate)
			}
			if snap.EURRUBF.OfficialRate != nil {
				ev = ev.Float64("eur_official", *snap.EURRUBF.OfficialRate)
			}
			if snap.EURRUBF.CBFunding != nil {
				ev = ev.Float64("eur_cb_funding", *snap.EURRUBF.CBFunding)
			}
			if snap.EURRUBF.PredictedFunding != nil {
				ev = ev.Float64("eur_predicted", *snap.EURRUBF.PredictedFunding)
			}
			ev.Time("ts", snap.Timestamp).Msg("funding snapshot")
			latest = &snap
		case <-writeTimer.C:
			if latest == nil {
				continue
			}
			if err := w.store.InsertSnapshot(ctx, *latest); err != nil {
				w.log.Warn().Err(err).Msg("snapshot insert failed")
			}
			latest = nil
		}
	}
}
