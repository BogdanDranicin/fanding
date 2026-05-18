package forex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/source"
)

const defaultBaseURL = "https://api.twelvedata.com"

// symbolMap maps internal source constants to TwelveData symbol strings.
var symbolMap = map[string]string{
	source.SymbolEURUSD: "EUR/USD",
	source.SymbolUSDCNH: "USD/CNH",
}

// Source implements source.MarketDataSource by polling the TwelveData /price endpoint.
type Source struct {
	apiKey       string
	baseURL      string
	pollInterval time.Duration
	logger       zerolog.Logger
	httpClient   *http.Client
	cancel       context.CancelFunc
	mu           sync.Mutex
	started      bool
	lastValues   sync.Map // internal symbol -> float64
}

// New creates a Source that polls TwelveData at the given interval.
func New(apiKey string, pollInterval time.Duration, logger zerolog.Logger) *Source {
	return newSource(apiKey, defaultBaseURL, pollInterval, logger)
}

// NewWithBaseURL creates a Source against a custom base URL (useful in tests).
func NewWithBaseURL(apiKey, baseURL string, pollInterval time.Duration, logger zerolog.Logger) *Source {
	return newSource(apiKey, baseURL, pollInterval, logger)
}

func newSource(apiKey, baseURL string, pollInterval time.Duration, logger zerolog.Logger) *Source {
	return &Source{
		apiKey:       apiKey,
		baseURL:      baseURL,
		pollInterval: pollInterval,
		logger:       logger,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:    5,
				IdleConnTimeout: 90 * time.Second,
			},
		},
	}
}

// Name implements source.MarketDataSource.
func (s *Source) Name() string { return "twelvedata" }

// Subscribe starts polling TwelveData for the given symbols.
// Returns an error if apiKey is empty or an unknown symbol is requested.
func (s *Source) Subscribe(ctx context.Context, symbols []string) (<-chan source.Tick, error) {
	if s.apiKey == "" {
		return nil, errors.New("twelvedata: TWELVEDATA_API_KEY is required but not set")
	}

	tdSymbols := make([]string, 0, len(symbols))
	for _, sym := range symbols {
		td, ok := symbolMap[sym]
		if !ok {
			return nil, fmt.Errorf("twelvedata: unknown symbol %q", sym)
		}
		tdSymbols = append(tdSymbols, td)
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil, errors.New("twelvedata: already subscribed; create a new Source to subscribe again")
	}
	s.started = true
	ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	ch := make(chan source.Tick, len(symbols)*4)
	go func() {
		defer close(ch)
		s.pollLoop(ctx, symbols, tdSymbols, ch)
	}()
	return ch, nil
}

// Close cancels polling goroutines started by Subscribe.
func (s *Source) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

// priceEntry is a per-symbol entry in the TwelveData /price response.
type priceEntry struct {
	Price   string `json:"price"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

func (s *Source) pollLoop(ctx context.Context, internalSyms, tdSyms []string, ch chan<- source.Tick) {
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	log := s.logger.With().Str("source", "twelvedata").Logger()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prices, err := s.fetchPrices(ctx, tdSyms)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Warn().Err(err).Msg("fetch failed")
				}
				continue
			}

			for i, internalSym := range internalSyms {
				entry, ok := prices[tdSyms[i]]
				if !ok {
					continue
				}
				if entry.Status == "error" || entry.Code != 0 {
					log.Warn().Str("symbol", tdSyms[i]).Str("message", entry.Message).Msg("symbol error from API")
					continue
				}
				price, err := strconv.ParseFloat(entry.Price, 64)
				if err != nil || price == 0 {
					continue
				}
				if prev, loaded := s.lastValues.Load(internalSym); loaded && prev.(float64) == price {
					continue
				}
				s.lastValues.Store(internalSym, price)

				select {
				case ch <- source.Tick{
					Symbol:    internalSym,
					Price:     price,
					Kind:      source.KindLastPrice,
					Timestamp: time.Now(),
					Source:    s.Name(),
				}:
				default:
				}
			}
			log.Debug().Msg("polled")
		}
	}
}

func (s *Source) fetchPrices(ctx context.Context, tdSymbols []string) (map[string]priceEntry, error) {
	url := fmt.Sprintf("%s/price?symbol=%s&apikey=%s",
		s.baseURL,
		strings.Join(tdSymbols, ","),
		s.apiKey,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	return parsePriceResponse(body, tdSymbols)
}

// parsePriceResponse handles both TwelveData response shapes:
//   - single symbol: {"price":"1.085","..."}
//   - multiple symbols: {"EUR/USD":{"price":"1.085"},"USD/CNH":{"price":"7.24"}}
func parsePriceResponse(body []byte, tdSymbols []string) (map[string]priceEntry, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	// Single-symbol response has "price" (or "code") as a top-level key.
	if _, isSingle := raw["price"]; isSingle || raw[tdSymbols[0]] == nil {
		var entry priceEntry
		if err := json.Unmarshal(body, &entry); err != nil {
			return nil, fmt.Errorf("unmarshal single: %w", err)
		}
		return map[string]priceEntry{tdSymbols[0]: entry}, nil
	}

	result := make(map[string]priceEntry, len(raw))
	for k, v := range raw {
		var entry priceEntry
		if err := json.Unmarshal(v, &entry); err != nil {
			continue
		}
		result[k] = entry
	}
	return result, nil
}
