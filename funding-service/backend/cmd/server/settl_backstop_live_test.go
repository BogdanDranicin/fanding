//go:build livescan

// Живая проверка страховки ноги фьючерса: лента MOEX ISS читается за день целиком
// и из неё собирается нога окна 10:00–15:30. Обычными прогонами не запускается —
// нужен тег и сеть, а после 15:45 МСК ещё и торговый день:
//
//	go test -tags livescan -run TestLiveSettlRebuild -v ./cmd/server/ -symbols USDRUBF,EURRUBF
//
// Сверять результат нужно с биржевым SWAPRATE того же дня: он равен
// clamp(нога − курс ЦБ на завтра, ±0.0015 × PREVSETTLEPRICE) с мёртвой зоной
// 0.001 × PREVSETTLEPRICE. Тест печатает всё, что для этой сверки нужно.
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/funding-service/backend/internal/source/moexiss"
)

// rebuildDefaultSymbols — что читать, когда -symbols не задан. Свой флаг здесь
// не заводится: -symbols уже объявлен живой проверкой потока брокера, и вторая
// регистрация того же имени роняет весь тестовый бинарник пакета.
const rebuildDefaultSymbols = "USDRUBF,EURRUBF"

func TestLiveSettlRebuild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := moexiss.NewClient()
	day := time.Now().In(mskZone).Format("2006-01-02")

	want := *liveSymbols
	if want == "" {
		want = rebuildDefaultSymbols
	}
	for _, sym := range strings.Split(want, ",") {
		sym = strings.TrimSpace(sym)
		if sym == "" {
			continue
		}
		trades, err := client.FetchTradesSince(ctx, "futures", "forts", sym, 0)
		if err != nil && len(trades) == 0 {
			t.Fatalf("%s: лента не прочитана: %v", sym, err)
		}
		leg, ok := windowVWAP(trades, day)
		if !ok {
			t.Errorf("%s: нога за %s не собралась (сделок в окне %d, за 15:30 %d)",
				sym, day, leg.prints, leg.post)
			continue
		}
		t.Logf("%s %s: нога = %.5f, объём окна = %.0f лотов, сделок в окне = %d, за 15:30 = %d",
			sym, day, leg.vwap, leg.volume, leg.prints, leg.post)
	}
}
