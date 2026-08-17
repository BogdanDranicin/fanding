//go:build livescan

// Живая проверка детектора на настоящей ленте MOEX. Обычными прогонами тестов не
// запускается (нужен тег и сеть):
//
//	go test -tags livescan -run TestLiveScan -v ./internal/robots/ -symbols SBER,GAZP
package robots

import (
	"context"
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/source/moexiss"
)

var (
	liveSymbols = flag.String("symbols", "SBER,GAZP,LKOH,VTBR,futures:USDRUBF",
		"тикеры для живой проверки, формат ROBOTS_SYMBOLS")
	livePages  = flag.Int("pages", 4, "сколько страниц ленты (по 5000 сделок) снимать с хвоста")
	liveWindow = flag.Duration("window", 0, "окно анализа; 0 — как в DefaultConfig")
	liveAt     = flag.String("at", "", "конец окна анализа, MSK ЧЧ:ММ; пусто — конец ленты")
	liveRun    = flag.Duration("live", 90*time.Second, "сколько крутить живой коллектор")
)

func TestLiveScan(t *testing.T) {
	feeds, err := ParseFeeds(*liveSymbols)
	if err != nil {
		t.Fatalf("ParseFeeds: %v", err)
	}

	cfg := DefaultConfig()
	if *liveWindow > 0 {
		cfg.Window = *liveWindow
	}
	client := moexiss.NewClient()
	det := NewDetector(cfg)
	// Разбор задним числом: аварийный предел длины ленты снимаем, иначе от
	// многочасового хвоста останется только его конец и окно окажется пустым.
	det.maxLen = 1 << 30
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var newest time.Time
	for _, f := range feeds {
		trades, err := client.FetchTradeTail(ctx, moexiss.TradeFeed{
			Engine: f.Engine, Market: f.Market, Board: f.Board, SecID: f.SecID,
		}, *livePages)
		if err != nil {
			t.Fatalf("%s: FetchLatestTrades: %v", f.Symbol, err)
		}
		var kept int
		for _, tr := range trades {
			if tr.OffMarket || tr.Backdated || tr.Timestamp.IsZero() {
				continue
			}
			side := Side(tr.Side)
			if side != SideBuy && side != SideSell {
				continue
			}
			det.Add(Print{
				TradeNo: tr.TradeNo, Symbol: f.Symbol, Time: tr.Timestamp,
				Price: tr.Price, Qty: tr.Quantity, Side: side,
			})
			kept++
			if tr.Timestamp.After(newest) {
				newest = tr.Timestamp
			}
		}
		var span string
		if len(trades) > 0 {
			span = trades[0].Timestamp.Format("15:04:05") + "–" + trades[len(trades)-1].Timestamp.Format("15:04:05")
		}
		t.Logf("%-10s принтов %5d (в работу %5d) %s", f.Symbol, len(trades), kept, span)
	}

	at := newest
	if *liveAt != "" {
		msk := time.FixedZone("MSK", 3*60*60)
		hm, err := time.Parse("15:04", *liveAt)
		if err != nil {
			t.Fatalf("флаг -at: %v", err)
		}
		y, m, d := newest.In(msk).Date()
		at = time.Date(y, m, d, hm.Hour(), hm.Minute(), 0, 0, msk)
	}
	t.Logf("окно анализа: %s, конец %s", cfg.Window, at.Format("15:04:05"))

	found := det.Scan(at)
	for _, f := range feeds {
		w := det.window(det.tapes[f.Symbol], at)
		var span string
		if len(w) > 0 {
			span = w[0].Time.Format("15:04:05") + "–" + w[len(w)-1].Time.Format("15:04:05")
		}
		t.Logf("  в окне %-10s %5d принтов %s", f.Symbol, len(w), span)
	}
	report(t, "боевые пороги", found)

	// Контрольный прогон с ослабленными порогами: показывает, что детектор
	// отбрасывает, и не задраны ли настройки по умолчанию.
	loose := cfg
	loose.MinUnitBeatRatio = 0.35
	loose.MaxJitter = 0.30
	loose.MinMatchRatio = 0.40
	det2 := NewDetector(loose)
	for _, f := range feeds {
		det2.tapes[f.Symbol] = det.tapes[f.Symbol]
	}
	report(t, "ослабленные пороги", det2.Scan(at))
}

func report(t *testing.T, title string, found []Robot) {
	t.Logf("%s — найдено роботов: %d", title, len(found))
	for _, r := range found {
		t.Logf("  %-8s %-5s лотовка %.0f–%.0f (типично %.0f)  период %.2f с  принтов %d  разброс %.3f  уверенность %.2f  %s–%s",
			r.Symbol, sideRu(r.Side), r.QtyMin, r.QtyMax, r.QtyTypical, r.PeriodSec,
			r.Prints, r.Jitter, r.Confidence,
			r.FirstSeen.Format("15:04:05"), r.LastSeen.Format("15:04:05"))
	}
}

func sideRu(s Side) string {
	if s == SideBuy {
		return "лонг"
	}
	return strings.TrimSpace("шорт")
}

// TestLiveCollector гоняет настоящий коллектор против живого ISS: проверяет весь
// путь целиком — курсор TRADENO, разбор ленты, детектор, реестр сессий.
//
//	go test -tags livescan -run TestLiveCollector -v ./internal/robots/ -live 90s
func TestLiveCollector(t *testing.T) {
	feeds, err := ParseFeeds(*liveSymbols)
	if err != nil {
		t.Fatalf("ParseFeeds: %v", err)
	}

	opts := DefaultCollectorOptions()
	opts.Feeds = feeds
	opts.ScanInterval = 10 * time.Second

	// store = nil: живая проверка не должна ничего писать в базу.
	c := NewCollector(moexiss.NewClient(), nil, opts, zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), *liveRun)
	defer cancel()
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	<-done

	c.mu.Lock()
	for _, f := range feeds {
		t.Logf("  лента %-10s %d принтов", f.Symbol, c.det.TapeLen(f.Symbol))
	}
	c.mu.Unlock()

	snap := c.Snapshot()
	t.Logf("роботов в срезе: %d", len(snap))
	for _, s := range snap {
		t.Logf("  %-8s %-5s лотовка %.0f–%.0f  период %.2f с  принтов %d  уверенность %.2f  активен %v",
			s.Symbol, sideRu(s.Side), s.QtyMin, s.QtyMax, s.PeriodSec, s.Prints, s.Confidence, s.Active)
	}
}
