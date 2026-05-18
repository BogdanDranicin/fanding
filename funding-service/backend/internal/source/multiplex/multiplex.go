package multiplex

import (
	"context"
	"fmt"
	"sync"

	"github.com/funding-service/backend/internal/source"
)

// Compile-time interface check.
var _ source.MarketDataSource = (*Source)(nil)

// Source fans in ticks from multiple MarketDataSource implementations.
// Each symbol is routed to exactly one underlying source via the routing map.
type Source struct {
	routing map[string]source.MarketDataSource
	all     []source.MarketDataSource // deduplicated, for Close
}

// New creates a Source from an explicit symbol→source routing map.
func New(routing map[string]source.MarketDataSource) *Source {
	seen := make(map[source.MarketDataSource]bool)
	var all []source.MarketDataSource
	for _, src := range routing {
		if !seen[src] {
			seen[src] = true
			all = append(all, src)
		}
	}
	return &Source{routing: routing, all: all}
}

// DefaultRouting returns the canonical symbol→source mapping for production.
// Pass the three live sources in order: moex, forex, cbr.
func DefaultRouting(moex, forex, cbr source.MarketDataSource) map[string]source.MarketDataSource {
	return map[string]source.MarketDataSource{
		source.SymbolUSDRUBF:        moex,
		source.SymbolEURRUBF:        moex,
		source.SymbolCNYRUBF:        moex,
		source.SymbolUSDTRUB:        moex,
		source.SymbolEURUSD:         forex,
		source.SymbolUSDCNH:         forex,
		source.SymbolUSDRubOfficial: cbr,
		source.SymbolEURRubOfficial: cbr,
	}
}

// Name implements source.MarketDataSource.
func (s *Source) Name() string { return "multiplex" }

// Subscribe routes the requested symbols to their respective sources, subscribes
// each (batching symbols belonging to the same source into one call), then
// fans all tick channels into a single output channel.
// Returns an error if any symbol has no registered source or any Subscribe fails.
func (s *Source) Subscribe(ctx context.Context, symbols []string) (<-chan source.Tick, error) {
	// Group requested symbols by owner source. Using a slice-of-pairs to preserve
	// insertion order and avoid non-determinism in ranging over maps.
	type group struct {
		src   source.MarketDataSource
		syms  []string
	}
	order := make([]group, 0)
	index := make(map[source.MarketDataSource]int)

	for _, sym := range symbols {
		src, ok := s.routing[sym]
		if !ok {
			return nil, fmt.Errorf("multiplex: no source registered for symbol %q", sym)
		}
		if i, exists := index[src]; exists {
			order[i].syms = append(order[i].syms, sym)
		} else {
			index[src] = len(order)
			order = append(order, group{src: src, syms: []string{sym}})
		}
	}

	// Subscribe each source to its symbol batch.
	channels := make([]<-chan source.Tick, 0, len(order))
	for _, g := range order {
		ch, err := g.src.Subscribe(ctx, g.syms)
		if err != nil {
			return nil, fmt.Errorf("multiplex: subscribe %s: %w", g.src.Name(), err)
		}
		channels = append(channels, ch)
	}

	// Fan-in: one forwarding goroutine per source channel.
	out := make(chan source.Tick, len(channels)*8)
	var wg sync.WaitGroup
	for _, ch := range channels {
		wg.Add(1)
		go func(c <-chan source.Tick) {
			defer wg.Done()
			for {
				select {
				case tick, ok := <-c:
					if !ok {
						return
					}
					select {
					case out <- tick:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}(ch)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out, nil
}

// Close calls Close on every unique underlying source.
// Returns the first error encountered; continues closing remaining sources.
func (s *Source) Close() error {
	var firstErr error
	for _, src := range s.all {
		if err := src.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
