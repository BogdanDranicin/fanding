package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/funding-service/backend/internal/config"
	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/source"
	"github.com/funding-service/backend/internal/source/cbr"
	"github.com/funding-service/backend/internal/source/moexiss"
	"github.com/funding-service/backend/internal/source/multiplex"
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

	moexSrc := moexiss.New(time.Duration(cfg.MOEXPollMs)*time.Millisecond, log.Logger)
	cbrSrc := cbr.New(log.Logger)

	routing := map[string]source.MarketDataSource{
		source.SymbolUSDRUBF:        moexSrc,
		source.SymbolEURRUBF:        moexSrc,
		source.SymbolCNYRUBF:        moexSrc,
		source.SymbolUSDTRUB:        moexSrc,
		source.SymbolUSDRubOfficial: cbrSrc,
		source.SymbolEURRubOfficial: cbrSrc,
	}
	mux := multiplex.New(routing)

	symbols := []string{
		source.SymbolUSDRUBF,
		source.SymbolEURRUBF,
		source.SymbolCNYRUBF,
		source.SymbolUSDTRUB,
		source.SymbolUSDRubOfficial,
		source.SymbolEURRubOfficial,
	}

	eng := funding.NewEngine()
	snapshots := make(chan funding.FundingSnapshot, 16)
	runner := funding.NewRunner(mux, eng, symbols, time.Second, snapshots)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(ctx) }()

	go func() {
		for snap := range snapshots {
			log.Info().
				Float64("usdrubf_vwap", snap.USDRUBF.VWAP).
				Float64("eurrubf_vwap", snap.EURRUBF.VWAP).
				Float64("cnyrubf_vwap", snap.CNYRUBF.VWAP).
				Float64("usdtrub_price", snap.USDTRUBPrice).
				Time("ts", snap.Timestamp).
				Msg("funding snapshot")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info().Msg("shutting down")
	cancel()
	<-runDone
}
