package cbr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/metrics"
	"github.com/funding-service/backend/internal/source"
)

const (
	defaultURL = "https://www.cbr.ru/scripts/XML_daily.asp"
	mskOffset  = 3 * 60 * 60 // UTC+3
)

// fetchInfo carries per-poll diagnostics for the observability log: which channel
// won the race (newest publication date) and how long each channel took.
type fetchInfo struct {
	winner    string
	latencies map[string]time.Duration
}

// Source implements source.MarketDataSource for the CBR official FX rate.
//
// In production (New) it RACES several CBR origin channels (official XML + SOAP)
// in parallel on every poll and emits the one whose publication date is newest,
// so a fresh rate is delivered as soon as the FASTEST channel reflects it rather
// than waiting for the slow, heavily-cached XML_daily endpoint. It uses an
// adaptive poll interval: 1 s during the 16:00–19:00 Moscow publication window,
// 5 minutes outside it. OnNewPublication is signalled on each new daily publication.
type Source struct {
	url        string
	logger     zerolog.Logger
	httpClient *http.Client
	intervalFn func() time.Duration

	// channels is the racing set (production). When empty, the source fetches the
	// single url instead (used by NewWithURL for deterministic tests).
	channels []Channel

	// OnNewPublication is signalled when a date change is detected in the CBR response.
	// It has a buffer of 1 so a slow consumer does not block polling.
	OnNewPublication chan time.Time

	cancel   context.CancelFunc
	mu       sync.Mutex
	started  bool
	lastDate string // written only from the single pollLoop goroutine

	// pubInfo holds the last detected publication for the audit journal, guarded by mu.
	pubInfo PublicationInfo
}

// PublicationInfo describes the most recently detected CBR publication: the
// rate's effective date ("DD.MM.YYYY"), the published rates taken from the
// winning channel's own response, the channel that won the race and its fetch
// latency. Carrying the rates here (rather than reading them back from the
// funding engine) avoids a race: the KindNewOfficialRate ticks reach the engine
// asynchronously, so at signal time the engine may still hold the previous rate.
type PublicationInfo struct {
	Date      string
	USD       float64
	EUR       float64
	CNY       float64
	Winner    string
	LatencyMs int64
}

// LastPublicationInfo returns the most recently detected publication.
// Zero value before the first publication. It is written before
// OnNewPublication is signalled, so a consumer woken by that channel
// always observes at least the publication that woke it.
func (s *Source) LastPublicationInfo() PublicationInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pubInfo
}

// New creates a production Source that races the CBR origin channels.
func New(logger zerolog.Logger) *Source {
	s := newSource(defaultURL, MoscowAdaptiveInterval, logger)
	s.httpClient = DirectClient()
	s.channels = FastChannels()
	return s
}

// NewWithURL creates a single-endpoint Source against a custom URL and interval
// function (for tests). It does not race channels.
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
// 1 s without code changes. On transient errors (network not yet ready at container start)
// it retries every retryInterval up to maxRetries times before falling back to intervalFn.
func (s *Source) pollLoop(ctx context.Context, symbols []string, ch chan<- source.Tick) {
	const retryInterval = 1 * time.Second
	const maxRetries = 5

	log := s.logger.With().Str("source", "cbr").Logger()
	failures := 0

	for {
		iv := s.intervalFn()

		fetchStart := time.Now()
		res, info, err := s.fetch(ctx)
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
		if res.Date == s.lastDate {
			log.Debug().Str("date", res.Date).Msg("no new publication")
		} else {
			// Различаем ДВА разных события, которые раньше смешивались в одном флаге:
			//
			//   rateIsFresh    — курс в ответе новее того, что знал движок. На холодном
			//                    старте это дата ЦБ из будущего (публикация уже прошла),
			//                    в рабочем режиме — смена даты. По нему тик помечается
			//                    KindNewOfficialRate, чтобы движок пересчитал фандинг.
			//
			//   livePublication — мы СВОИМИ ГЛАЗАМИ увидели смену даты, уже работая
			//                    (lastDate != ""). Только это настоящее «событие
			//                    публикации»: его время осмысленно. Его и шлём в журнал
			//                    и Telegram. Холодный старт (после 16:30 или в выходной)
			//                    публикацией НЕ считается — время было бы временем
			//                    рестарта, а не публикации. Именно это раньше засоряло
			//                    журнал фантомными строками в полночь и по выходным.
			wasCold := s.lastDate == ""
			rateIsFresh := !wasCold || isFutureDate(res.Date)
			livePublication := !wasCold
			s.lastDate = res.Date
			s.emitTicks(res, symbols, ch, rateIsFresh)

			// Реальную публикацию логируем на Warn, чтобы событие было видно в проде
			// (LOG_LEVEL=warn). Диагностика задержки: канал-победитель гонки, латентность
			// каждого канала, действующий интервал опроса и время МСК.
			ev := log.Info()
			if livePublication {
				ev = log.Warn()
			}
			ev.Str("date", res.Date).
				Bool("is_update", livePublication).
				Bool("cold_start", wasCold).
				Str("winner", info.winner).
				Interface("channel_latency_ms", latencyMillis(info.latencies)).
				Dur("fetch_latency", fetchDur).
				Dur("poll_interval", iv).
				Str("msk_time", time.Now().In(time.FixedZone("MSK", mskOffset)).Format("15:04:05")).
				Msg("cbr rates emitted")

			if livePublication {
				s.mu.Lock()
				s.pubInfo = PublicationInfo{
					Date:   res.Date,
					USD:    res.USD,
					EUR:    res.EUR,
					CNY:    res.CNY,
					Winner: info.winner,
				}
				if l, ok := info.latencies[info.winner]; ok {
					s.pubInfo.LatencyMs = l.Milliseconds()
				}
				s.mu.Unlock()
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

// fetch returns the freshest rates. In race mode it queries every channel in
// parallel and returns the result with the newest publication date; in single
// mode it fetches the configured url.
func (s *Source) fetch(ctx context.Context) (ChannelResult, fetchInfo, error) {
	if len(s.channels) == 0 {
		return s.fetchSingle(ctx)
	}
	return s.fetchRace(ctx)
}

func (s *Source) fetchRace(ctx context.Context) (ChannelResult, fetchInfo, error) {
	type outcome struct {
		id  string
		res ChannelResult
		dur time.Duration
		err error
	}
	results := make([]outcome, len(s.channels))
	var wg sync.WaitGroup
	for i, ch := range s.channels {
		wg.Add(1)
		go func(i int, ch Channel) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			start := time.Now()
			res, err := ch.Fetch(cctx, s.httpClient)
			results[i] = outcome{ch.ID, res, time.Since(start), err}
		}(i, ch)
	}
	wg.Wait()

	info := fetchInfo{latencies: make(map[string]time.Duration, len(results))}
	var best ChannelResult
	var bestDate time.Time
	found := false
	var firstErr error
	for _, o := range results {
		info.latencies[o.id] = o.dur
		if o.err != nil {
			if firstErr == nil {
				firstErr = o.err
			}
			continue
		}
		d := parseRateDate(o.res.Date)
		if !found || d.After(bestDate) {
			found, bestDate, best, info.winner = true, d, o.res, o.id
		}
	}
	if !found {
		return ChannelResult{}, info, firstErr
	}
	return best, info, nil
}

func (s *Source) fetchSingle(ctx context.Context) (ChannelResult, fetchInfo, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, s.url, nil)
	if err != nil {
		return ChannelResult{}, fetchInfo{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; funding-service/1.0)")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return ChannelResult{}, fetchInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ChannelResult{}, fetchInfo{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	res, err := decodeValCurs(resp.Body)
	info := fetchInfo{
		winner:    "cbr_official_xml",
		latencies: map[string]time.Duration{"cbr_official_xml": time.Since(start)},
	}
	return res, info, err
}

func (s *Source) emitTicks(res ChannelResult, symbols []string, ch chan<- source.Tick, isNew bool) {
	wanted := make(map[string]bool, len(symbols))
	for _, sym := range symbols {
		wanted[sym] = true
	}

	kind := source.KindOfficialRate
	if isNew {
		kind = source.KindNewOfficialRate
	}

	emit := func(sym string, price float64) {
		if price <= 0 || !wanted[sym] {
			return
		}
		select {
		case ch <- source.Tick{
			Symbol:    sym,
			Price:     price,
			Kind:      kind,
			Timestamp: time.Now(),
			Source:    s.Name(),
		}:
		default:
		}
	}

	emit(source.SymbolUSDRubOfficial, res.USD)
	emit(source.SymbolEURRubOfficial, res.EUR)
}

// latencyMillis converts a per-channel duration map to milliseconds for logging.
func latencyMillis(in map[string]time.Duration) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v.Milliseconds()
	}
	return out
}

// parseRateDate parses a CBR "DD.MM.YYYY" date; on failure returns the zero time
// so an unparseable date never wins the race against a valid one.
func parseRateDate(s string) time.Time {
	t, err := time.Parse("02.01.2006", s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// isFutureDate reports whether the CBR date string ("DD.MM.YYYY") is strictly after today MSK.
// A future date means CBR has already published fresh rates for the next business day.
func isFutureDate(cbrDate string) bool {
	t := parseRateDate(cbrDate)
	if t.IsZero() {
		return false
	}
	now := time.Now().In(time.FixedZone("MSK", mskOffset))
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return t.Truncate(24 * time.Hour).After(today)
}

// MoscowAdaptiveInterval is the production interval function.
// It returns 1 s during 16:00–19:00 Moscow time (CBR publication window), 5 minutes otherwise.
func MoscowAdaptiveInterval() time.Duration {
	return AdaptiveInterval(time.Now().In(time.FixedZone("MSK", mskOffset)))
}

// AdaptiveInterval computes the poll interval for a given Moscow time.
// Exported so it can be unit-tested without depending on real wall time.
func AdaptiveInterval(t time.Time) time.Duration {
	h, _, _ := t.Clock()
	// CBR publishes next-day rates between ~16:30 and 18:00 MSK.
	// Poll every 1 s in the extended window 16:00–19:00 so a new rate is delivered
	// within ~1 s of whichever channel publishes first. The window is wider than the
	// typical publication time to catch late releases.
	if h >= 16 && h < 19 {
		return 1 * time.Second
	}
	return 5 * time.Minute
}
