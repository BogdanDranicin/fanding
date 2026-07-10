package funding_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/source"
)

// controlledSource lets the test push ticks and control channel lifetime.
type controlledSource struct {
	ch  chan source.Tick
	err error
}

func newControlledSource() *controlledSource {
	return &controlledSource{ch: make(chan source.Tick, 16)}
}

func (s *controlledSource) Name() string { return "controlled" }
func (s *controlledSource) Subscribe(_ context.Context, _ []string) (<-chan source.Tick, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.ch, nil
}
func (s *controlledSource) Close() error { return nil }

func runnerWithSource(src source.MarketDataSource) (*funding.Runner, chan funding.FundingSnapshot) {
	eng := funding.NewEngine()
	out := make(chan funding.FundingSnapshot, 32)
	r := funding.NewRunner(src, eng, []string{source.SymbolUSDRUBF}, 20*time.Millisecond, out)
	return r, out
}

func TestRunner_SnapshotsDelivered(t *testing.T) {
	src := newControlledSource()
	runner, out := runnerWithSource(src)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	// Wait for at least one snapshot before cancelling.
	select {
	case <-out:
	case <-ctx.Done():
		t.Fatal("no snapshot received before timeout")
	}

	cancel()
	<-done
}

func TestRunner_TicksIngested(t *testing.T) {
	src := newControlledSource()
	eng := funding.NewEngine()
	out := make(chan funding.FundingSnapshot, 32)
	runner := funding.NewRunner(src, eng, []string{source.SymbolUSDRUBF}, 20*time.Millisecond, out)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	// Push two ticks before Run: Volume is cumulative VOLTODAY, so the rolling VWAP
	// needs a second tick (100→110) to have an attributable increment of 10 @82 → VWAP=82.
	now := time.Now()
	src.ch <- source.Tick{
		Symbol:    source.SymbolUSDRUBF,
		Price:     82.0,
		Volume:    100,
		Kind:      source.KindLastPrice,
		Timestamp: now,
		Source:    "moex-iss",
	}
	src.ch <- source.Tick{
		Symbol:    source.SymbolUSDRUBF,
		Price:     82.0,
		Volume:    110,
		Kind:      source.KindLastPrice,
		Timestamp: now.Add(time.Millisecond),
		Source:    "moex-iss",
	}

	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	// Wait until a snapshot carries a non-zero VWAP.
	var got funding.FundingSnapshot
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case snap := <-out:
			if snap.USDRUBF.VWAP != 0 {
				got = snap
				cancel()
				goto check
			}
		case <-deadline:
			t.Fatal("VWAP never became non-zero")
		}
	}
check:
	<-done
	if got.USDRUBF.VWAP != 82.0 {
		t.Errorf("VWAP: want 82.0, got %v", got.USDRUBF.VWAP)
	}
}

func TestRunner_ContextCancelStopsRun(t *testing.T) {
	src := newControlledSource()
	runner, _ := runnerWithSource(src)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("unexpected error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("Run did not return after context cancel")
	}
}

func TestRunner_SubscribeErrorPropagated(t *testing.T) {
	src := &controlledSource{err: errors.New("subscribe failed")}
	runner, _ := runnerWithSource(src)

	err := runner.Run(context.Background())
	if err == nil {
		t.Fatal("expected error from failed Subscribe")
	}
}

func TestRunner_SourceChannelCloseStillWaitsForSnapshots(t *testing.T) {
	// Source closes its channel immediately; snapshot goroutine keeps running
	// until ctx is cancelled. Verify Run doesn't return prematurely.
	src := newControlledSource()
	close(src.ch) // close immediately — ingest goroutine finishes right away

	runner, out := runnerWithSource(src)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	// Snapshot goroutine should still be running; wait for a snapshot.
	select {
	case <-out:
		// snapshot arrived even though tick source is closed
	case err := <-done:
		t.Fatalf("Run returned early with err=%v before any snapshot", err)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected snapshot even after source channel closed")
	}

	cancel()
	<-done
}
