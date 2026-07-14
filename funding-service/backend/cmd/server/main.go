package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/funding-service/backend/internal/api"
	"github.com/funding-service/backend/internal/config"
	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/journal"
	"github.com/funding-service/backend/internal/metrics"
	"github.com/funding-service/backend/internal/source"
	"github.com/funding-service/backend/internal/source/cbr"
	"github.com/funding-service/backend/internal/source/moexiss"
	"github.com/funding-service/backend/internal/source/multiplex"
	"github.com/funding-service/backend/internal/storage"
	tgbot "github.com/funding-service/backend/internal/telegram"
	appws "github.com/funding-service/backend/internal/ws"
)

func main() {
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("config load failed")
	}

	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	log.Logger = log.Output(os.Stdout)

	log.Info().
		Int("port", cfg.Port).
		Int("moex_poll_ms", cfg.MOEXPollMs).
		Str("log_level", cfg.LogLevel).
		Msg("service starting")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := storage.Migrate(cfg.DSN()); err != nil {
		log.Fatal().Err(err).Msg("db migrations failed")
	}
	log.Info().Msg("db migrations ok")

	pool, err := storage.Connect(ctx, cfg.DSN())
	if err != nil {
		log.Fatal().Err(err).Msg("db connect failed")
	}
	defer pool.Close()
	log.Info().Msg("db connected")

	store := storage.NewStore(pool)

	moexSrc := moexiss.New(time.Duration(cfg.MOEXPollMs)*time.Millisecond, log.Logger)
	cbrSrc := cbr.New(log.Logger)

	routing := map[string]source.MarketDataSource{
		source.SymbolUSDRUBF:        moexSrc,
		source.SymbolEURRUBF:        moexSrc,
		source.SymbolCNYRUBF:        moexSrc,
		source.SymbolUSDRubTOM:      moexSrc,
		source.SymbolUSDRubOfficial: cbrSrc,
		source.SymbolEURRubOfficial: cbrSrc,
	}
	mux := multiplex.New(routing)

	// USDTRUB (crypto) has no source yet; omitted until a crypto feed is added.
	// USDRUB_TOM is the spot "tomorrow" leg whose 10:00–15:30 MSK WAPRICE predicts the CBR fixing.
	// EUR/RUB_TOM doesn't trade on MOEX — EUR predicted CB rate uses EUR/USD × USD/RUB cross.
	symbols := []string{
		source.SymbolUSDRUBF,
		source.SymbolEURRUBF,
		source.SymbolCNYRUBF,
		source.SymbolUSDRubTOM,
		source.SymbolUSDRubOfficial,
		source.SymbolEURRubOfficial,
	}

	eng := funding.NewEngine()
	seedEffectiveRates(ctx, store, eng)
	snapshots := make(chan funding.FundingSnapshot, 16)
	runner := funding.NewRunner(mux, eng, symbols, time.Second, snapshots)

	tickObsCh := make(chan source.Tick, 1000)
	runner.SetTickObserver(tickObsCh, log.Logger)

	writer := storage.NewWriter(store, tickObsCh, snapshots, log.Logger)

	hub := appws.NewHub(log.Logger)

	apiRouter := api.NewRouter(
		store,
		cfg.TelegramBotName,
		cfg.AllowedOrigin,
		log.Logger,
		moexSrc.GetSpecs,
	)

	router := http.NewServeMux()

	router.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.Handle("GET /metrics", promhttp.Handler())

	// Live snapshot as JSON for the cbrwatch latency tracker (more specific than
	// "/api/" so it takes precedence over the chi router).
	router.HandleFunc("GET /api/v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		data, err := appws.SnapshotJSON(eng.Snapshot())
		if err != nil {
			http.Error(w, "encode error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	})

	router.Handle("/api/", apiRouter)

	router.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		wsOpts := &websocket.AcceptOptions{}
		if cfg.AllowedOrigin != "*" && cfg.AllowedOrigin != "" {
			wsOpts.OriginPatterns = []string{cfg.AllowedOrigin}
		} else {
			wsOpts.InsecureSkipVerify = true
			log.Warn().Str("ALLOWED_ORIGIN", cfg.AllowedOrigin).
				Msg("WS origin check disabled — set ALLOWED_ORIGIN=<frontend-url> in production")
		}
		conn, err := websocket.Accept(w, r, wsOpts)
		if err != nil {
			log.Warn().Err(err).Str("remote", r.RemoteAddr).Msg("ws accept failed")
			return
		}

		c := appws.NewClient(conn, hub, r.RemoteAddr, log.Logger)
		if !hub.Register(c) {
			conn.Close(websocket.StatusTryAgainLater, "server at capacity")
			log.Warn().Str("remote", r.RemoteAddr).Msg("ws rejected: server at capacity")
			return
		}
		log.Info().Str("remote", r.RemoteAddr).Int("clients", hub.Len()).Msg("ws client connected")

		connCtx, connCancel := context.WithCancel(ctx)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			defer connCancel()
			c.ReadPump(connCtx)
		}()
		go func() {
			defer wg.Done()
			c.WritePump(connCtx)
		}()
		wg.Wait()

		hub.Unregister(c)
		log.Info().Str("remote", r.RemoteAddr).Int("clients", hub.Len()).Msg("ws client disconnected")
	})

	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	const wsBroadcastInterval = 250 * time.Millisecond
	go func() {
		ticker := time.NewTicker(wsBroadcastInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				snap := eng.Snapshot()
				data, err := appws.EncodeSnapshot(snap)
				if err != nil {
					log.Warn().Err(err).Msg("ws encode snapshot failed")
					continue
				}
				metrics.SnapshotLatency.Observe(time.Since(snap.Timestamp).Seconds())
				hub.Broadcast(data)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Persist a daily audit row for every CBR publication (journal page).
	journalPubCh := make(chan time.Time, 1)
	recorder := journal.New(store, eng.Snapshot, cbrSrc.LastPublicationInfo, log.Logger)
	go recorder.Run(ctx, journalPubCh)

	// Fan-out cbrSrc.OnNewPublication: always count metric and journal it;
	// forward to the Telegram dispatcher too if the bot is enabled.
	dispPubCh := make(chan time.Time, 1)
	go func() {
		fwd := func(ch chan time.Time, t time.Time) {
			select {
			case ch <- t:
			default:
			}
		}
		for {
			select {
			case t, ok := <-cbrSrc.OnNewPublication:
				if !ok {
					return
				}
				metrics.CBPublications.Inc()
				fwd(journalPubCh, t)
				fwd(dispPubCh, t)
			case <-ctx.Done():
				return
			}
		}
	}()

	if cfg.TelegramToken != "" {
		// Bot init can block for a while on proxy dials (api.telegram.org is
		// unreachable from some networks, so each proxy is tried with a timeout).
		// Run the whole bot lifecycle in the background so a slow or dead proxy
		// never delays the HTTP server or data collection.
		go func() {
			const retryEvery = 30 * time.Second
			for {
				bot, err := tgbot.New(cfg.TelegramToken, cfg.TelegramProxyURLs, pool, log.Logger)
				if err != nil {
					// Proxies to api.telegram.org can be flaky (rotating IPs, transient
					// Bad Gateway). Keep retrying so the bot connects on its own the
					// moment a proxy can reach Telegram, without a manual restart.
					log.Warn().Err(err).Dur("retry_in", retryEvery).Msg("telegram bot init failed — retrying")
					select {
					case <-ctx.Done():
						return
					case <-time.After(retryEvery):
						continue
					}
				}
				go bot.Run(ctx)
				disp := tgbot.NewDispatcher(bot, pool, eng.Snapshot, log.Logger)
				disp.Run(ctx, eng.SettlementCh(), dispPubCh) // blocks until ctx is cancelled
				return
			}
		}()
	} else {
		log.Info().Msg("TELEGRAM_BOT_TOKEN not set — bot disabled")
	}

	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(ctx) }()
	go writer.Run(ctx)

	go func() {
		log.Info().Int("port", cfg.Port).Msg("http server listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("http server failed")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info().Msg("shutting down")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		log.Warn().Err(err).Msg("http shutdown error")
	}

	cancel()
	<-runDone
}

// seedEffectiveRates передаёт движку курсы ЦБ, ДЕЙСТВУЮЩИЕ сегодня, из журнала
// публикаций: последняя публикация с датой РАНЬШЕ сегодняшней — это курс,
// вступивший в силу сегодня. Без засева движок после рестарта, случившегося
// уже после публикации ЦБ, не знает вчерашний курс, а от него MOEX масштабирует
// границы K1/K2 формулы фандинга (сверено 14.07: SWAPRATE −0.11493 =
// −0.0015 × 76.6213). Ошибки не фатальны — движок деградирует к новому курсу.
func seedEffectiveRates(ctx context.Context, store *storage.Store, eng *funding.Engine) {
	rows, err := store.RecentCBPublications(ctx, 7)
	if err != nil {
		log.Warn().Err(err).Msg("effective CBR rates seed: db read failed")
		return
	}
	msk := time.FixedZone("MSK", 3*60*60)
	today := time.Now().In(msk).Format("2006-01-02")
	for _, r := range rows { // newest first
		if r.Date.Format("2006-01-02") >= today {
			continue // сегодняшняя публикация — курс на завтра, не на сегодня
		}
		rates := make(map[string]float64, 2)
		if r.USDRate != nil {
			rates[source.SymbolUSDRubOfficial] = *r.USDRate
		}
		if r.EURRate != nil {
			rates[source.SymbolEURRubOfficial] = *r.EURRate
		}
		if len(rates) == 0 {
			return
		}
		eng.SeedEffectiveRates(today, rates)
		log.Info().Str("published", r.Date.Format("2006-01-02")).
			Interface("rates", rates).Msg("effective CBR rates seeded from journal")
		return
	}
	log.Warn().Msg("effective CBR rates seed: no past publication found in journal")
}
