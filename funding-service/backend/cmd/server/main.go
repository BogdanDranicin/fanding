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
		source.SymbolUSDRubOfficial: cbrSrc,
		source.SymbolEURRubOfficial: cbrSrc,
	}
	mux := multiplex.New(routing)

	// USDTRUB (crypto) has no source yet; omitted until a crypto feed is added.
	symbols := []string{
		source.SymbolUSDRUBF,
		source.SymbolEURRUBF,
		source.SymbolCNYRUBF,
		source.SymbolUSDRubOfficial,
		source.SymbolEURRubOfficial,
	}

	eng := funding.NewEngine()
	snapshots := make(chan funding.FundingSnapshot, 16)
	runner := funding.NewRunner(mux, eng, symbols, time.Second, snapshots)

	tickObsCh := make(chan source.Tick, 1000)
	runner.SetTickObserver(tickObsCh, log.Logger)

	writer := storage.NewWriter(store, tickObsCh, snapshots, log.Logger)

	hub := appws.NewHub(log.Logger)

	apiRouter := api.NewRouter(store, cfg.TelegramBotName, log.Logger)

	router := http.NewServeMux()

	router.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.Handle("GET /metrics", promhttp.Handler())
	router.Handle("/api/", apiRouter)

	router.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true, // TODO: restrict origins in production
		})
		if err != nil {
			log.Warn().Err(err).Str("remote", r.RemoteAddr).Msg("ws accept failed")
			return
		}

		c := appws.NewClient(conn, hub, r.RemoteAddr, log.Logger)
		hub.Register(c)
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
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: router,
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

	// Fan-out cbrSrc.OnNewPublication: always count metric; forward to dispatcher if bot is enabled.
	dispPubCh := make(chan time.Time, 1)
	go func() {
		for {
			select {
			case t, ok := <-cbrSrc.OnNewPublication:
				if !ok {
					return
				}
				metrics.CBPublications.Inc()
				select {
				case dispPubCh <- t:
				default:
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	if cfg.TelegramToken != "" {
		bot, err := tgbot.New(cfg.TelegramToken, pool, log.Logger)
		if err != nil {
			log.Warn().Err(err).Msg("telegram bot init failed — running without bot")
		} else {
			go bot.Run(ctx)
			disp := tgbot.NewDispatcher(bot, pool, eng.Snapshot, log.Logger)
			go disp.Run(ctx, dispPubCh)
		}
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
