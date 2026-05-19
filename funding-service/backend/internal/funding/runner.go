package funding

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/metrics"
	"github.com/funding-service/backend/internal/source"
)

// Runner wires a MarketDataSource to a funding Engine, continuously ingesting
// ticks and publishing FundingSnapshots at a fixed interval.
type Runner struct {
	src              source.MarketDataSource
	engine           *Engine
	symbols          []string
	snapshotInterval time.Duration
	out              chan<- FundingSnapshot
	tickObs          chan source.Tick
	obsLog           zerolog.Logger
}

// NewRunner creates a Runner. symbols is the list passed to src.Subscribe.
// out receives a FundingSnapshot every snapshotInterval; slow consumers drop ticks.
func NewRunner(
	src source.MarketDataSource,
	engine *Engine,
	symbols []string,
	snapshotInterval time.Duration,
	out chan<- FundingSnapshot,
) *Runner {
	return &Runner{
		src:              src,
		engine:           engine,
		symbols:          symbols,
		snapshotInterval: snapshotInterval,
		out:              out,
	}
}

// SetTickObserver wires an optional channel that receives a copy of every ingested tick.
// When the channel is full the oldest entry is dropped and a warning is logged.
// Must be called before Run.
func (r *Runner) SetTickObserver(ch chan source.Tick, log zerolog.Logger) {
	r.tickObs = ch
	r.obsLog = log
}

// sendToObs forwards tick to the observer channel without blocking.
// If the buffer is full, the oldest entry is dropped first.
func (r *Runner) sendToObs(tick source.Tick) {
	if r.tickObs == nil {
		return
	}
	select {
	case r.tickObs <- tick:
	default:
		// full: drop oldest, enqueue newest
		select {
		case <-r.tickObs:
		default:
		}
		select {
		case r.tickObs <- tick:
		default:
		}
		r.obsLog.Warn().Str("symbol", tick.Symbol).Msg("tick observer buffer full, dropped oldest")
	}
}

// Run subscribes to ticks, feeds them to the engine, and emits periodic snapshots.
// It blocks until ctx is cancelled, then waits for both internal goroutines to finish.
// Returns ctx.Err() on clean cancellation, or a subscription error.
func (r *Runner) Run(ctx context.Context) error {
	ticks, err := r.src.Subscribe(ctx, r.symbols)
	if err != nil {
		return fmt.Errorf("runner: subscribe: %w", err)
	}

	var wg sync.WaitGroup

	// Ingest goroutine: reads ticks until source channel closes or ctx is cancelled.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case tick, ok := <-ticks:
				if !ok {
					return
				}
				r.engine.Ingest(tick)
				r.sendToObs(tick)
				metrics.TicksReceived.WithLabelValues(tick.Source, tick.Symbol).Inc()
			case <-ctx.Done():
				return
			}
		}
	}()

	// Snapshot goroutine: publishes at snapshotInterval until ctx is done.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(r.snapshotInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				snap := r.engine.Snapshot()
				select {
				case r.out <- snap:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	wg.Wait()
	return ctx.Err()
}
