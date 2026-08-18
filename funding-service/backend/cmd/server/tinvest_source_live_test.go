//go:build livescan

// Сквозная живая проверка: каталог брокера → поток обезличенных сделок → детектор
// роботов, вместе с запасной лентой ISS.
//
//	go test -tags livescan -run TestLiveTInvestPipeline -v ./cmd/server/ \
//	    -token "$TINVEST_TOKEN" -live 90s
package main

import (
	"context"
	"flag"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/robots"
	"github.com/funding-service/backend/internal/source/moexiss"
	"github.com/funding-service/backend/internal/source/tinvest"
)

var (
	liveToken   = flag.String("token", "", "токен T-Invest API")
	liveSymbols = flag.String("symbols", "", "тикеры через запятую; пусто — правила по умолчанию")
	liveRun     = flag.Duration("live", 90*time.Second, "сколько крутить сбор")
)

func TestLiveTInvestPipeline(t *testing.T) {
	if *liveToken == "" {
		t.Skip("нужен -token")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *liveRun+2*time.Minute)
	defer cancel()

	client, err := tinvest.Dial(ctx, tinvest.Config{Token: *liveToken, AppName: "funding-service/robots-live"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	watch, err := robots.NewWatchlist(*liveSymbols)
	if err != nil {
		t.Fatalf("NewWatchlist: %v", err)
	}

	opts := robots.DefaultCollectorOptions()
	opts.Watch = watch
	opts.Stream = newTInvestSource(client)
	opts.StreamRetry = tinvest.ReconnectDelay
	opts.ScanInterval = 15 * time.Second

	// store = nil: живая проверка ничего не пишет в базу.
	c := robots.NewCollector(moexiss.NewClient(), nil, opts, zerolog.Nop())

	runCtx, stop := context.WithTimeout(ctx, *liveRun)
	defer stop()
	done := make(chan struct{})
	go func() { c.Run(runCtx); close(done) }()
	<-done

	symbols := c.Symbols()
	t.Logf("инструментов в ленте: %d", len(symbols))

	days := c.DayVolumes()
	t.Logf("инструментов с накопленным оборотом: %d", len(days))

	snap := c.Snapshot()
	t.Logf("роботов в срезе: %d", len(snap))
	for i, s := range snap {
		if i >= 10 {
			t.Logf("  ... ещё %d", len(snap)-10)
			break
		}
		side := "шорт"
		if s.Side == robots.SideBuy {
			side = "лонг"
		}
		mark := "подтв."
		if s.Provisional {
			mark = "предв."
		}
		t.Logf("  %-9s %-5s %s лотовка %.0f  период %.2f с  принтов %d  сила %.2f%%  удар в %s",
			s.Symbol, side, mark, s.QtyTypical, s.PeriodSec, s.Prints, s.StrengthPct,
			s.NextBeatAt.Format("15:04:05"))
	}

	if len(symbols) == 0 {
		t.Skip("лента пуста — вероятно, торги закрыты")
	}
}
