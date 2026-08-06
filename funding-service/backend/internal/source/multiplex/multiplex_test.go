package multiplex_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/funding-service/backend/internal/source"
	"github.com/funding-service/backend/internal/source/multiplex"
)

// --- mock helpers ---

// staticSource delivers a fixed batch of ticks then closes the channel.
type staticSource struct {
	name   string
	ticks  []source.Tick
	closed atomic.Bool
}

func (m *staticSource) Name() string { return m.name }
func (m *staticSource) Subscribe(_ context.Context, _ []string) (<-chan source.Tick, error) {
	ch := make(chan source.Tick, len(m.ticks))
	for _, t := range m.ticks {
		ch <- t
	}
	close(ch)
	return ch, nil
}
func (m *staticSource) Close() error { m.closed.Store(true); return nil }

// blockingSource holds the channel open until ctx is cancelled.
type blockingSource struct{ name string }

func (m *blockingSource) Name() string { return m.name }
func (m *blockingSource) Subscribe(ctx context.Context, _ []string) (<-chan source.Tick, error) {
	ch := make(chan source.Tick)
	go func() { defer close(ch); <-ctx.Done() }()
	return ch, nil
}
func (m *blockingSource) Close() error { return nil }

// capturingSource records which symbol batches were passed to Subscribe.
type capturingSource struct {
	name       string
	subscribed [][]string
}

func (m *capturingSource) Name() string { return m.name }
func (m *capturingSource) Subscribe(_ context.Context, syms []string) (<-chan source.Tick, error) {
	cp := make([]string, len(syms))
	copy(cp, syms)
	m.subscribed = append(m.subscribed, cp)
	ch := make(chan source.Tick)
	close(ch)
	return ch, nil
}
func (m *capturingSource) Close() error { return nil }

// --- tests ---

func TestMultiplex_Name(t *testing.T) {
	mux := multiplex.New(map[string]source.MarketDataSource{})
	if mux.Name() != "multiplex" {
		t.Errorf("expected multiplex, got %s", mux.Name())
	}
}

func TestMultiplex_FansInFromTwoSources(t *testing.T) {
	src1 := &staticSource{name: "src1", ticks: []source.Tick{
		{Symbol: source.SymbolUSDRUBF, Price: 81.0, Kind: source.KindLastPrice, Source: "src1"},
	}}
	src2 := &staticSource{name: "src2", ticks: []source.Tick{
		{Symbol: source.SymbolUSDRubOfficial, Price: 1.08, Kind: source.KindLastPrice, Source: "src2"},
	}}

	mux := multiplex.New(map[string]source.MarketDataSource{
		source.SymbolUSDRUBF: src1,
		source.SymbolUSDRubOfficial:  src2,
	})

	ch, err := mux.Subscribe(context.Background(), []string{source.SymbolUSDRUBF, source.SymbolUSDRubOfficial})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	seen := make(map[string]float64)
	for tick := range ch {
		seen[tick.Symbol] = tick.Price
	}

	if seen[source.SymbolUSDRUBF] != 81.0 {
		t.Errorf("USDRUBF: want 81.0, got %v", seen[source.SymbolUSDRUBF])
	}
	if seen[source.SymbolUSDRubOfficial] != 1.08 {
		t.Errorf("USDRUB_CB: want 1.08, got %v", seen[source.SymbolUSDRubOfficial])
	}
}

func TestMultiplex_SymbolsBatchedPerSource(t *testing.T) {
	src := &capturingSource{name: "moex"}
	mux := multiplex.New(map[string]source.MarketDataSource{
		source.SymbolUSDRUBF: src,
		source.SymbolEURRUBF: src,
		source.SymbolCNYRUBF: src,
	})

	if _, err := mux.Subscribe(context.Background(),
		[]string{source.SymbolUSDRUBF, source.SymbolEURRUBF, source.SymbolCNYRUBF}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// All three symbols should be passed in a single Subscribe call, not three.
	if len(src.subscribed) != 1 {
		t.Errorf("expected 1 Subscribe call (batched), got %d", len(src.subscribed))
	}
	if len(src.subscribed[0]) != 3 {
		t.Errorf("expected 3 symbols in batch, got %d: %v", len(src.subscribed[0]), src.subscribed[0])
	}
}

func TestMultiplex_UnknownSymbolReturnsError(t *testing.T) {
	mux := multiplex.New(map[string]source.MarketDataSource{
		source.SymbolUSDRUBF: &staticSource{name: "x"},
	})
	_, err := mux.Subscribe(context.Background(), []string{"UNKNOWN"})
	if err == nil {
		t.Fatal("expected error for unknown symbol")
	}
}

func TestMultiplex_ContextCancelClosesChannel(t *testing.T) {
	src1 := &blockingSource{name: "a"}
	src2 := &blockingSource{name: "b"}

	mux := multiplex.New(map[string]source.MarketDataSource{
		source.SymbolUSDRUBF: src1,
		source.SymbolUSDRubOfficial:  src2,
	})

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := mux.Subscribe(ctx, []string{source.SymbolUSDRUBF, source.SymbolUSDRubOfficial})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	cancel()

	select {
	case <-ch:
		// channel drained and closed
	case <-time.After(500 * time.Millisecond):
		t.Error("output channel not closed after context cancel")
	}
}

func TestMultiplex_CloseCallsUnderlyingSources(t *testing.T) {
	src1 := &staticSource{name: "x"}
	src2 := &staticSource{name: "y"}

	mux := multiplex.New(map[string]source.MarketDataSource{
		source.SymbolUSDRUBF: src1,
		source.SymbolUSDRubOfficial:  src2,
	})

	if err := mux.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !src1.closed.Load() {
		t.Error("src1 not closed")
	}
	if !src2.closed.Load() {
		t.Error("src2 not closed")
	}
}

func TestMultiplex_DefaultRoutingCoversAllSymbols(t *testing.T) {
	moex := &capturingSource{name: "moex"}
	cbr := &capturingSource{name: "cbr"}

	routing := multiplex.DefaultRouting(moex, cbr)

	allSymbols := []string{
		source.SymbolUSDRUBF,
		source.SymbolEURRUBF,
		source.SymbolCNYRUBF,
		source.SymbolUSDTRUB,
		source.SymbolUSDRubTOM,
		source.SymbolUSDRubOfficial,
		source.SymbolEURRubOfficial,
	}
	for _, sym := range allSymbols {
		if _, ok := routing[sym]; !ok {
			t.Errorf("symbol %q missing from DefaultRouting", sym)
		}
	}

	// Spot-check assignments.
	if routing[source.SymbolUSDRUBF] != moex {
		t.Error("USDRUBF should map to moex")
	}
	if routing[source.SymbolUSDRubTOM] != moex {
		t.Error("USDRUB_TOM should map to moex")
	}
	if routing[source.SymbolUSDRubOfficial] != cbr {
		t.Error("USDRubOfficial should map to cbr")
	}
}
