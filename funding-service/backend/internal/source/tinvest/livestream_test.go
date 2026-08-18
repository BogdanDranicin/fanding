//go:build livescan

// Живая проверка стрима T-Invest. Обычными прогонами не запускается: нужен тег,
// сеть и токен.
//
//	go test -tags livescan -run TestLiveStream -v ./internal/source/tinvest/ \
//	    -token "$TINVEST_TOKEN" -symbols SBER,GAZP,USDRUBF -live 30s
package tinvest

import (
	"context"
	"flag"
	"strings"
	"testing"
	"time"
)

var (
	liveToken   = flag.String("token", "", "токен T-Invest API")
	liveSymbols = flag.String("symbols", "SBER,GAZP,LKOH,USDRUBF,CNYRUBF,IMOEXF", "тикеры через запятую")
	liveRun     = flag.Duration("live", 30*time.Second, "сколько слушать поток")
)

func TestLiveStream(t *testing.T) {
	if *liveToken == "" {
		t.Skip("нужен -token")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *liveRun+time.Minute)
	defer cancel()

	c, err := Dial(ctx, Config{Token: *liveToken, AppName: "funding-service/robots-probe"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	instruments, err := c.Instruments(ctx)
	if err != nil {
		t.Fatalf("Instruments: %v", err)
	}
	t.Logf("инструментов в каталоге: %d", len(instruments))

	want := map[string]bool{}
	for _, s := range strings.Split(*liveSymbols, ",") {
		want[strings.ToUpper(strings.TrimSpace(s))] = true
	}
	byUID := map[string]string{}
	var uids []string
	for _, in := range instruments {
		if want[strings.ToUpper(in.Ticker)] {
			byUID[in.UID] = in.Ticker
			uids = append(uids, in.UID)
		}
	}
	if len(uids) == 0 {
		t.Fatalf("ни один из тикеров %q не найден в каталоге", *liveSymbols)
	}
	t.Logf("подписываемся на %d инструментов", len(uids))

	out := make(chan Trade, 4096)
	streamCtx, stop := context.WithTimeout(ctx, *liveRun)
	defer stop()
	go func() {
		if err := c.StreamTrades(streamCtx, uids, byUID, out); err != nil && streamCtx.Err() == nil {
			t.Logf("стрим оборвался: %v", err)
		}
		close(out)
	}()

	var (
		count    int
		subSec   int
		perSym   = map[string]int{}
		maxLag   time.Duration
		sumLag   time.Duration
		firstAt  time.Time
		lastSeen time.Time
	)
	for tr := range out {
		count++
		perSym[tr.Ticker]++
		if firstAt.IsZero() {
			firstAt = time.Now()
		}
		lastSeen = tr.Time
		// Задержка — от биржевой метки сделки до момента, когда её увидел процесс.
		lag := time.Since(tr.Time)
		sumLag += lag
		if lag > maxLag {
			maxLag = lag
		}
		if tr.Time.Nanosecond() != 0 {
			subSec++
		}
	}

	if count == 0 {
		t.Skip("за окно наблюдения не пришло ни одной сделки — вероятно, торги закрыты")
	}
	t.Logf("сделок получено: %d", count)
	t.Logf("по инструментам: %v", perSym)
	t.Logf("задержка от метки биржи: средняя %s, худшая %s",
		(sumLag / time.Duration(count)).Round(time.Millisecond), maxLag.Round(time.Millisecond))
	t.Logf("последняя сделка помечена биржей: %s", lastSeen.Format("15:04:05.000"))
	t.Logf("меток точнее секунды: %d из %d — %s",
		subSec, count, precisionVerdict(subSec, count))
}

// precisionVerdict отвечает на главный вопрос к новому источнику: даёт ли он
// время сделки точнее секунды. От этого зависит, нужна ли детектору поправка на
// дискретность биржевой метки.
func precisionVerdict(subSec, total int) string {
	switch {
	case subSec == 0:
		return "метка дискретна по секунде, как в ленте ISS"
	case subSec*2 >= total:
		return "метка точнее секунды — арифметика тактов может считаться напрямую"
	default:
		return "часть меток дробная, часть ровная — доверять точности нельзя"
	}
}
