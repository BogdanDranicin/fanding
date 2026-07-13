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

// InstrumentSpec holds static contract parameters from the MOEX ISS securities block.
type InstrumentSpec struct {
	Symbol        string  `json:"symbol"`
	InitialMargin float64 `json:"initial_margin"` // ГО per lot in rubles
	LotSize       float64 `json:"lot_size"`        // contract size (e.g. 1000 for currency futures)
	StepPrice     float64 `json:"step_price"`      // rubles per minimum step
	MinStep       float64 `json:"min_step"`         // minimum price step
}

type securityParams struct {
	engine, market, board string
	// secid is the MOEX ISS security identifier used in the request path. When empty,
	// the internal symbol itself is used (true for futures, whose SECID equals the symbol).
	secid string
	// trades enables the per-deal poller (trades.json) that feeds exact VWAP.
	trades bool
}

// knownSecurities maps symbol constants to their MOEX ISS path parameters.
// Spot "tomorrow" instruments live on currency/selt/CETS and have a SECID distinct
// from the internal symbol; ticks are re-tagged with the internal symbol on emit.
var knownSecurities = map[string]securityParams{
	source.SymbolUSDRUBF:    {engine: "futures", market: "forts", board: "", trades: true},
	source.SymbolEURRUBF:    {engine: "futures", market: "forts", board: "", trades: true},
	source.SymbolCNYRUBF:    {engine: "futures", market: "forts", board: "", trades: true},
	source.SymbolUSDRubTOM:  {engine: "currency", market: "selt", board: "CETS", secid: "USD000UTSTOM"},
}

// defaultTradePollInterval paces the incremental trades poller. One request per
// second per symbol keeps us well inside ISS limits; each request only returns
// deals newer than the stored TRADENO cursor.
const defaultTradePollInterval = time.Second

// Source implements source.MarketDataSource by polling MOEX ISS REST API.
type Source struct {
	client            *Client
	pollInterval      time.Duration
	tradePollInterval time.Duration
	logger            zerolog.Logger
	cancel            context.CancelFunc
	mu                sync.Mutex
	started           bool
	lastValues        sync.Map // "symbol:field" -> float64, for deduplication
	specs             sync.Map // symbol -> InstrumentSpec
}

// New creates a Source that polls MOEX ISS at the given interval.
func New(pollInterval time.Duration, logger zerolog.Logger) *Source {
	return NewWithClient(NewClient(), pollInterval, logger)
}

// NewWithClient creates a Source using the provided Client (useful in tests).
func NewWithClient(client *Client, pollInterval time.Duration, logger zerolog.Logger) *Source {
	return &Source{
		client:            client,
		pollInterval:      pollInterval,
		tradePollInterval: defaultTradePollInterval,
		logger:            logger,
	}
}

// SetTradePollInterval overrides the trades poller pace. Must be called before Subscribe.
func (s *Source) SetTradePollInterval(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started && d > 0 {
		s.tradePollInterval = d
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
		if knownSecurities[sym].trades {
			wg.Add(1)
			go func(symbol string) {
				defer wg.Done()
				s.pollTrades(ctx, symbol, ch)
			}(sym)
		}
	}
	go func() {
		wg.Wait()
		close(ch)
	}()
	return ch, nil
}

// GetSpecs returns a snapshot of the latest instrument specs received from MOEX ISS.
func (s *Source) GetSpecs() map[string]InstrumentSpec {
	result := make(map[string]InstrumentSpec)
	s.specs.Range(func(k, v any) bool {
		result[k.(string)] = v.(InstrumentSpec)
		return true
	})
	return result
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

	// secid identifies the security in the request path; for futures it equals the symbol.
	secid := symbol
	if params.secid != "" {
		secid = params.secid
	}

	log := s.logger.With().Str("source", "moex-iss").Str("symbol", symbol).Logger()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			resp, err := s.client.FetchSecurity(ctx, params.board, params.market, params.engine, secid)
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

			if resp.Securities != nil {
				s.updateSpec(symbol, resp.Securities)
			}

			s.maybeEmit(ch, symbol, "LAST", source.KindLastPrice, resp.MarketData, vol, ts)
			s.maybeEmit(ch, symbol, "BID", source.KindBid, resp.MarketData, vol, ts)
			s.maybeEmit(ch, symbol, "OFFER", source.KindAsk, resp.MarketData, vol, ts)
			s.maybeEmit(ch, symbol, "SETTLEPRICE", source.KindSettlePrice, resp.MarketData, vol, ts)
			s.maybeEmit(ch, symbol, "WAPRICE", source.KindWaprice, resp.MarketData, 0, ts)
			s.emitSwapRate(ch, symbol, resp.MarketData, ts)
			log.Debug().Msg("polled")
		}
	}
}

// pollTrades incrementally pulls executed deals from trades.json and emits one
// KindTrade tick per deal. The TRADENO cursor starts at 0, so the first pull
// backfills the whole current session — the engine's rolling VWAP is correct
// immediately after a restart instead of reseeding from zero.
func (s *Source) pollTrades(ctx context.Context, symbol string, ch chan<- source.Tick) {
	params := knownSecurities[symbol]
	secid := symbol
	if params.secid != "" {
		secid = params.secid
	}
	log := s.logger.With().Str("source", "moex-iss-trades").Str("symbol", symbol).Logger()

	ticker := time.NewTicker(s.tradePollInterval)
	defer ticker.Stop()

	var lastTradeNo int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			trades, err := s.client.FetchTradesSince(ctx, params.engine, params.market, secid, lastTradeNo)
			if err != nil && !errors.Is(err, context.Canceled) {
				// Partial batches are still emitted below; the cursor advances with
				// them, so the next poll resumes exactly where this one failed.
				log.Warn().Err(err).Int64("tradeno", lastTradeNo).Msg("trades fetch failed")
			}
			for _, tr := range trades {
				if tr.TradeNo > lastTradeNo {
					lastTradeNo = tr.TradeNo
				}
				// Адресные сделки не участвуют в VWAP — они проходят вне стакана
				// по договорной цене (биржевой WAPRICE их тоже не учитывает).
				if tr.OffMarket {
					continue
				}
				// Blocking send: dropping a trade would silently lose its volume from
				// the VWAP forever. This goroutine has nothing else to do, so waiting
				// for the consumer is safe; ctx aborts the wait on shutdown.
				select {
				case ch <- source.Tick{
					Symbol:    symbol,
					Price:     tr.Price,
					Volume:    tr.Quantity,
					Kind:      source.KindTrade,
					Timestamp: tr.Timestamp,
					Source:    s.Name(),
				}:
				case <-ctx.Done():
					return
				}
			}
			if len(trades) > 0 {
				log.Debug().Int("trades", len(trades)).Int64("tradeno", lastTradeNo).Msg("trades polled")
			}
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

// emitSwapRate emits a KindSwapRate tick for the given symbol.
// Zero is treated as "not available" because MOEX ISS returns SWAPRATE=0 for continuous
// currency futures series (USDRUBF/EURRUBF), making 0 indistinguishable from missing data.
func (s *Source) emitSwapRate(ch chan<- source.Tick, symbol string, md map[string]interface{}, ts time.Time) {
	v, ok := md["SWAPRATE"]
	if !ok || v == nil {
		return
	}
	rate, ok := v.(float64)
	if !ok || rate == 0 {
		return
	}

	key := symbol + ":SWAPRATE"
	if prev, loaded := s.lastValues.Load(key); loaded && prev.(float64) == rate {
		return
	}
	s.lastValues.Store(key, rate)

	select {
	case ch <- source.Tick{
		Symbol:    symbol,
		Price:     rate,
		Kind:      source.KindSwapRate,
		Timestamp: ts,
		Source:    s.Name(),
	}:
	default:
	}
}

func (s *Source) updateSpec(symbol string, sec map[string]interface{}) {
	spec := InstrumentSpec{Symbol: symbol}
	if v, ok := sec["INITIALMARGIN"].(float64); ok && v > 0 {
		spec.InitialMargin = v
	}
	// MOEX ISS uses LOTVOLUME (int32 → parsed as float64 by JSON decoder)
	if v, ok := sec["LOTVOLUME"].(float64); ok && v > 0 {
		spec.LotSize = v
	}
	if v, ok := sec["STEPPRICE"].(float64); ok && v > 0 {
		spec.StepPrice = v
	}
	if v, ok := sec["MINSTEP"].(float64); ok && v > 0 {
		spec.MinStep = v
	}
	if spec.InitialMargin > 0 {
		s.specs.Store(symbol, spec)
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
	t, err := time.ParseInLocation(sysTimeLayout, str, time.FixedZone("MSK", 3*60*60))
	if err != nil {
		return time.Now()
	}
	return t
}
