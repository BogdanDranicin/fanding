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
	"github.com/funding-service/backend/internal/positions"
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

	posClient := positions.New()
	posRefresher := positions.NewRefresher(posClient, log.Logger)
	if conn, err := store.GetBrokerConnection(ctx); err == nil && conn != nil {
		posRefresher.Reload(conn.SSOSession, conn.DeviceID)
	}
	go posRefresher.Run(ctx)

	brokerAdapter := &brokerStoreAdapter{store: store, refresher: posRefresher}

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

	apiRouter := api.NewRouter(
		store,
		cfg.TelegramBotName,
		cfg.AllowedOrigin,
		log.Logger,
		posRefresher,
		&positionFetcherAdapter{client: posClient, httpClient: &http.Client{Timeout: 5 * time.Second}, log: log.Logger},
		brokerAdapter,
	)

	router := http.NewServeMux()

	router.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.Handle("GET /metrics", promhttp.Handler())
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

// brokerStoreAdapter связывает storage.Store и positions.Refresher с api.BrokerStore.
type brokerStoreAdapter struct {
	store     *storage.Store
	refresher *positions.Refresher
}

func (a *brokerStoreAdapter) UpsertBrokerConnection(ssoSession, deviceID, expiresAtStr string) error {
	expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
	if err != nil {
		return fmt.Errorf("parse expires_at: %w", err)
	}
	conn := storage.BrokerConnection{
		SSOSession: ssoSession,
		DeviceID:   deviceID,
		ExpiresAt:  expiresAt,
	}
	if err := a.store.UpsertBrokerConnection(context.Background(), conn); err != nil {
		return err
	}
	a.refresher.Reload(ssoSession, deviceID)
	return nil
}

func (a *brokerStoreAdapter) GetBrokerConnection() *api.BrokerConnectionStatus {
	conn, err := a.store.GetBrokerConnection(context.Background())
	if err != nil || conn == nil {
		return nil
	}
	return &api.BrokerConnectionStatus{
		Configured: true,
		ExpiresAt:  conn.ExpiresAt.UTC().Format("02.01.2006"),
	}
}

// positionFetcherAdapter адаптирует positions.Client к api.PositionFetcher.
type positionFetcherAdapter struct {
	client     *positions.Client
	httpClient *http.Client
	log        zerolog.Logger
}

func (a *positionFetcherAdapter) GetPositions(accessToken string) ([]api.PositionJSON, error) {
	ctx := context.Background()
	pos, err := a.client.GetPositions(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	result := make([]api.PositionJSON, len(pos))
	for i, p := range pos {
		var entryPrice float64
		if p.Pos > 0 {
			entryPrice = p.TotalBuy / float64(p.Pos)
		}

		pj := api.PositionJSON{
			Symbol:     p.Symbol,
			Exchange:   p.Exchange,
			Side:       p.Side,
			Pos:        p.Pos,
			EntryPrice: entryPrice,
			Date:       p.Date,
			Time:       p.Time,
			Asset:      p.Asset,
		}

		if entryPrice > 0 && p.Board != "" {
			cur, err := positions.FetchMOEXLastPrice(ctx, a.httpClient, p.Symbol, p.Board)
			if err != nil {
				a.log.Debug().Err(err).Str("symbol", p.Symbol).Msg("positions: moex price fetch failed")
			} else {
				pct := (cur - entryPrice) / entryPrice * 100
				profit := (cur - entryPrice) * float64(p.Pos)
				pj.CurrentPrice = &cur
				pj.UnrealizedProfitPct = &pct
				pj.UnrealizedProfit = &profit
			}
		}

		result[i] = pj
	}
	return result, nil
}
