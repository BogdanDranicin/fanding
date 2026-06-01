package cbr

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"

	"github.com/funding-service/backend/internal/metrics"
	"github.com/funding-service/backend/internal/source"
)

const (
	defaultURL = "https://www.cbr.ru/scripts/XML_daily.asp"
	mskOffset  = 3 * 60 * 60 // UTC+3
)

// knownCodes maps CBR CharCode values to internal source symbol constants.
var knownCodes = map[string]string{
	"USD": source.SymbolUSDRubOfficial,
	"EUR": source.SymbolEURRubOfficial,
}

// Source implements source.MarketDataSource for the CBR official FX rate.
// It uses an adaptive poll interval: 3 s during the 16:00–19:00 Moscow publication
// window, 5 minutes outside it. OnNewPublication receives a signal whenever a new
// daily publication is detected (for downstream Telegram alerts).
type Source struct {
	url        string
	logger     zerolog.Logger
	httpClient *http.Client
	intervalFn func() time.Duration

	// OnNewPublication is signalled when a date change is detected in the CBR response.
	// It has a buffer of 1 so a slow consumer does not block polling.
	OnNewPublication chan time.Time

	cancel   context.CancelFunc
	mu       sync.Mutex
	started  bool
	lastDate string // written only from the single pollLoop goroutine
}

// New creates a Source against the live CBR endpoint with adaptive polling.
func New(logger zerolog.Logger) *Source {
	return newSource(defaultURL, MoscowAdaptiveInterval, logger)
}

// NewWithURL creates a Source against a custom URL and interval function (for tests).
func NewWithURL(url string, intervalFn func() time.Duration, logger zerolog.Logger) *Source {
	return newSource(url, intervalFn, logger)
}

func newSource(url string, intervalFn func() time.Duration, logger zerolog.Logger) *Source {
	return &Source{
		url:              url,
		logger:           logger,
		intervalFn:       intervalFn,
		OnNewPublication: make(chan time.Time, 1),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:    3,
				IdleConnTimeout: 90 * time.Second,
			},
		},
	}
}

// Name implements source.MarketDataSource.
func (s *Source) Name() string { return "cbr" }

// Subscribe starts polling the CBR endpoint. Accepted symbols:
// source.SymbolUSDRubOfficial and source.SymbolEURRubOfficial.
func (s *Source) Subscribe(ctx context.Context, symbols []string) (<-chan source.Tick, error) {
	for _, sym := range symbols {
		if sym != source.SymbolUSDRubOfficial && sym != source.SymbolEURRubOfficial {
			return nil, fmt.Errorf("cbr: unknown symbol %q", sym)
		}
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil, errors.New("cbr: already subscribed")
	}
	s.started = true
	ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	ch := make(chan source.Tick, len(symbols)*2)
	go func() {
		defer close(ch)
		s.pollLoop(ctx, symbols, ch)
	}()
	return ch, nil
}

// Close cancels the polling goroutine.
func (s *Source) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

// pollLoop fetches immediately on startup, then sleeps for intervalFn() between requests.
// intervalFn is re-evaluated each iteration, so the interval can switch between 5 min and
// 200 ms without code changes. On transient errors (network not yet ready at container start)
// it retries every retryInterval up to maxRetries times before falling back to intervalFn.
func (s *Source) pollLoop(ctx context.Context, symbols []string, ch chan<- source.Tick) {
	const retryInterval = 3 * time.Second
	const maxRetries = 5

	log := s.logger.With().Str("source", "cbr").Logger()
	failures := 0

	for {
		iv := s.intervalFn()

		fetchStart := time.Now()
		vc, err := s.fetchRates(ctx)
		fetchDur := time.Since(fetchStart)
		metrics.CBRFetchDuration.Observe(fetchDur.Seconds())

		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Warn().Err(err).Dur("fetch_latency", fetchDur).Msg("fetch failed")
			failures++
			wait := retryInterval
			if failures > maxRetries {
				failures = 0
			}
			if iv < wait {
				wait = iv
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}

		failures = 0
		if vc.Date == s.lastDate {
			log.Debug().Str("date", vc.Date).Msg("no new publication")
		} else {
			// isNew=true если дата в ответе ЦБ позже сегодня (публикация уже состоялась,
			// в т.ч. при рестарте после 16:30 когда lastDate ещё пустой).
			isNew := s.lastDate != "" || isFutureDate(vc.Date)
			s.lastDate = vc.Date
			s.emitTicks(vc, symbols, ch, isNew)

			// Новую публикацию логируем на Warn, чтобы событие было видно в проде
			// (LOG_LEVEL=warn). Добавляем диагностику задержки: латентность HTTP-запроса,
			// действующий интервал опроса и время МСК — этого не хватало для разбора
			// «почему курсы пришли позже остальных».
			ev := log.Info()
			if isNew {
				ev = log.Warn()
			}
			ev.Str("date", vc.Date).
				Bool("is_update", isNew).
				Dur("fetch_latency", fetchDur).
				Dur("poll_interval", iv).
				Str("msk_time", time.Now().In(time.FixedZone("MSK", mskOffset)).Format("15:04:05")).
				Msg("cbr rates emitted")

			if isNew {
				select {
				case s.OnNewPublication <- time.Now():
				default:
				}
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(iv):
		}
	}
}

func (s *Source) emitTicks(vc *valCurs, symbols []string, ch chan<- source.Tick, isNew bool) {
	wantedSet := make(map[string]bool, len(symbols))
	for _, sym := range symbols {
		wantedSet[sym] = true
	}

	kind := source.KindOfficialRate
	if isNew {
		kind = source.KindNewOfficialRate
	}

	for _, v := range vc.Valutes {
		internalSym, ok := knownCodes[v.CharCode]
		if !ok || !wantedSet[internalSym] {
			continue
		}

		priceStr := strings.ReplaceAll(v.Value, ",", ".")
		price, err := strconv.ParseFloat(strings.TrimSpace(priceStr), 64)
		if err != nil || price == 0 {
			continue
		}
		if v.Nominal > 1 {
			price /= float64(v.Nominal)
		}

		select {
		case ch <- source.Tick{
			Symbol:    internalSym,
			Price:     price,
			Kind:      kind,
			Timestamp: time.Now(),
			Source:    s.Name(),
		}:
		default:
		}
	}
}

func (s *Source) fetchRates(ctx context.Context) (*valCurs, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; funding-service/1.0)")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	return parseXML(resp.Body)
}

// parseXML decodes a CBR XML response, transparently handling windows-1251 encoding
// via the xml.Decoder CharsetReader hook.
func parseXML(r io.Reader) (*valCurs, error) {
	dec := xml.NewDecoder(r)
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		if strings.EqualFold(charset, "windows-1251") {
			return transform.NewReader(input, charmap.Windows1251.NewDecoder()), nil
		}
		return input, nil
	}
	var vc valCurs
	if err := dec.Decode(&vc); err != nil {
		return nil, fmt.Errorf("decode xml: %w", err)
	}
	return &vc, nil
}

// valCurs is the root element of the CBR XML response.
type valCurs struct {
	XMLName xml.Name `xml:"ValCurs"`
	Date    string   `xml:"Date,attr"`
	Valutes []valute `xml:"Valute"`
}

type valute struct {
	CharCode string `xml:"CharCode"`
	Nominal  int    `xml:"Nominal"`
	Value    string `xml:"Value"`
}

// isFutureDate reports whether the CBR date string ("DD.MM.YYYY") is strictly after today MSK.
// A future date means CBR has already published fresh rates for the next business day.
func isFutureDate(cbrDate string) bool {
	t, err := time.Parse("02.01.2006", cbrDate)
	if err != nil {
		return false
	}
	now := time.Now().In(time.FixedZone("MSK", mskOffset))
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return t.Truncate(24 * time.Hour).After(today)
}

// MoscowAdaptiveInterval is the production interval function.
// It returns 3 s during 16:00–19:00 Moscow time (CBR publication window), 5 minutes otherwise.
func MoscowAdaptiveInterval() time.Duration {
	return AdaptiveInterval(time.Now().In(time.FixedZone("MSK", mskOffset)))
}

// AdaptiveInterval computes the poll interval for a given Moscow time.
// Exported so it can be unit-tested without depending on real wall time.
func AdaptiveInterval(t time.Time) time.Duration {
	h, _, _ := t.Clock()
	// CBR publishes next-day rates between ~16:30 and 18:00 MSK.
	// Poll every 3 s in the extended window 16:00–19:00 so the CB funding
	// appears within seconds of the publication rather than up to 30 s later.
	// The window is wider than the typical publication time to catch late releases.
	if h >= 16 && h < 19 {
		return 3 * time.Second
	}
	return 5 * time.Minute
}
