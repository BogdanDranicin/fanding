package moexiss

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/source"
)

const sysTimeLayout = "2006-01-02 15:04:05"

type securityParams struct {
	engine, market, board string
}

// knownSecurities maps symbol constants to their MOEX ISS path parameters.
var knownSecurities = map[string]securityParams{
	source.SymbolUSDRUBF: {engine: "futures", market: "forts", board: ""},
	source.SymbolEURRUBF: {engine: "futures", market: "forts", board: ""},
	source.SymbolCNYRUBF: {engine: "futures", market: "forts", board: ""},
}

// Source implements source.MarketDataSource by polling MOEX ISS REST API.
type Source struct {
	client       *Client
	pollInterval time.Duration
	logger       zerolog.Logger
	cancel       context.CancelFunc
	mu           sync.Mutex
	started      bool
	lastValues   sync.Map // "symbol:field" -> float64, for deduplication
}

// New creates a Source that polls MOEX ISS at the given interval.
func New(pollInterval time.Duration, logger zerolog.Logger) *Source {
	return NewWithClient(NewClient(), pollInterval, logger)
}

// NewWithClient creates a Source using the provided Client (useful in tests).
func NewWithClient(client *Client, pollInterval time.Duration, logger zerolog.Logger) *Source {
	return &Source{
		client:       client,
		pollInterval: pollInterval,
		logger:       logger,
	}
}

// Name implements source.MarketDataSource.
func (s *Source) Name() string { return "moex-iss" }

// Subscribe starts a polling goroutine per symbol and returns a channel of Ticks.
// The channel is closed when ctx is cancelled or Close is called.
// Returns an error for unknown symbols or if already subscribed.
func (s *Source) Subscribe(ctx context.Context, symbols []string) (<-chan source.Tick, error) {
	for _, sym := range symbols {
		if _, ok := knownSecurities[sym]; !ok {
			return nil, fmt.Errorf("moex-iss: unknown symbol %q", sym)
		}
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil, errors.New("moex-iss: already subscribed; create a new Source to subscribe again")
	}
	s.started = true
	ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	ch := make(chan source.Tick, len(symbols)*8)
	var wg sync.WaitGroup
	for _, sym := range symbols {
		wg.Add(1)
		go func(symbol string) {
			defer wg.Done()
			s.pollSymbol(ctx, symbol, ch)
		}(sym)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()
	return ch, nil
}

// Close cancels all polling goroutines started by Subscribe.
func (s *Source) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

func (s *Source) pollSymbol(ctx context.Context, symbol string, ch chan<- source.Tick) {
	params := knownSecurities[symbol]
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	log := s.logger.With().Str("source", "moex-iss").Str("symbol", symbol).Logger()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			resp, err := s.client.FetchSecurity(ctx, params.board, params.market, params.engine, symbol)
			if errors.Is(err, ErrNotModified) {
				log.Debug().Msg("not modified")
				continue
			}
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Warn().Err(err).Msg("fetch failed")
				}
				continue
			}

			ts := parseTime(resp.MarketData)
			vol, _ := resp.MarketData["VOLTODAY"].(float64)

			s.maybeEmit(ch, symbol, "LAST", source.KindLastPrice, resp.MarketData, vol, ts)
			s.maybeEmit(ch, symbol, "BID", source.KindBid, resp.MarketData, vol, ts)
			s.maybeEmit(ch, symbol, "OFFER", source.KindAsk, resp.MarketData, vol, ts)
			log.Debug().Msg("polled")
		}
	}
}

func (s *Source) maybeEmit(ch chan<- source.Tick, symbol, field string, kind source.TickKind, md map[string]interface{}, vol float64, ts time.Time) {
	v, ok := md[field]
	if !ok || v == nil {
		return
	}
	price, ok := v.(float64)
	if !ok || price == 0 {
		return
	}

	key := symbol + ":" + field
	if prev, loaded := s.lastValues.Load(key); loaded && prev.(float64) == price {
		return
	}
	s.lastValues.Store(key, price)

	select {
	case ch <- source.Tick{
		Symbol:    symbol,
		Price:     price,
		Volume:    vol,
		Kind:      kind,
		Timestamp: ts,
		Source:    s.Name(),
	}:
	default:
		// Channel full — skip tick rather than block polling goroutine.
	}
}

func parseTime(md map[string]interface{}) time.Time {
	v, ok := md["SYSTIME"]
	if !ok || v == nil {
		return time.Now()
	}
	str, ok := v.(string)
	if !ok {
		return time.Now()
	}
	t, err := time.ParseInLocation(sysTimeLayout, str, time.Local)
	if err != nil {
		return time.Now()
	}
	return t
}
