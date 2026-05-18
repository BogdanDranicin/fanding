package source

import "context"

// MarketDataSource is the unified interface for all price data providers.
// Implementations may use polling, streaming, or any other transport.
type MarketDataSource interface {
	// Name returns a human-readable identifier for this source.
	Name() string
	// Subscribe begins delivering ticks for the given symbols on the returned channel.
	// The channel is closed when ctx is cancelled or an unrecoverable error occurs.
	Subscribe(ctx context.Context, symbols []string) (<-chan Tick, error)
	// Close releases resources held by the source.
	Close() error
}
