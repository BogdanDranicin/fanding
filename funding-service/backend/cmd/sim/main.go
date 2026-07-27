// Command sim replays real historical market data through the production funding
// engine to validate CBFunding against the known-correct MOEX SWAPRATE.
//
// For each day it reconstructs the futures 10:00–15:30 leg from 1-minute candles
// (fed as KindTrade ticks, so the engine computes settlVWAP exactly as in prod),
// seeds the effective CBR rate (deadband base), freezes settlement, then publishes
// that day's CBR rate — reproducing the moment funding is fixed. It prints the
// engine's CBFunding next to the actual SWAPRATE from MOEX history.
//
// Timestamps are mapped onto the current wall-clock MSK date so the engine's
// time.Now()-based "today" gating (cbPublishedToday / effectiveRate) lines up.
//
// Run: go run ./cmd/sim <path-to-sim.json>
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/source"
)

var msk = time.FixedZone("MSK", 3*60*60)

type dayData struct {
	Day     string          `json:"day"`
	USDEff  float64         `json:"usd_eff"`
	EUREff  float64         `json:"eur_eff"`
	USDNew  float64         `json:"usd_new"`
	EURNew  float64         `json:"eur_new"`
	USDSwap float64         `json:"usd_swap"`
	EURSwap float64         `json:"eur_swap"`
	USDCnd  [][]interface{} `json:"usd_candles"` // [ "hh:mm", close, volume ]
	EURCnd  [][]interface{} `json:"eur_candles"`
}

func main() {
	path := "sim.json"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	var days []dayData
	if err := json.Unmarshal(b, &days); err != nil {
		panic(err)
	}

	today := time.Now().In(msk).Format("2006-01-02")
	fmt.Printf("Проверка реального движка (маппинг на %s). CBFunding vs фактический SWAPRATE.\n\n", today)
	fmt.Printf("%-11s %-4s %10s %10s %10s %11s %s\n", "день", "инс", "settlVWAP", "CBFunding", "SWAPRATE", "разница", "вердикт")

	var maxDiff float64
	for _, d := range days {
		eng := funding.NewEngine()
		eng.SeedEffectiveRates(today, map[string]float64{
			source.SymbolUSDRubOfficial: d.USDEff,
			source.SymbolEURRubOfficial: d.EUREff,
		})

		replay(eng, source.SymbolUSDRUBF, d.USDCnd, today)
		replay(eng, source.SymbolEURRUBF, d.EURCnd, today)

		// Публикация курсов ЦБ (после клиринга фьючерса) — момент фиксации фандинга.
		pub := tsAt(today, "16:56")
		eng.Ingest(source.Tick{Symbol: source.SymbolUSDRubOfficial, Price: d.USDNew, Kind: source.KindNewOfficialRate, Timestamp: pub})
		eng.Ingest(source.Tick{Symbol: source.SymbolEURRubOfficial, Price: d.EURNew, Kind: source.KindNewOfficialRate, Timestamp: pub})

		snap := eng.Snapshot()
		maxDiff = math.Max(maxDiff, report(d.Day, "USD", snap.USDRUBF, d.USDSwap))
		maxDiff = math.Max(maxDiff, report(d.Day, "EUR", snap.EURRUBF, d.EURSwap))
	}
	fmt.Printf("\nМаксимальная разница движок↔факт по всем дням: %.5f\n", maxDiff)
}

// replay feeds a symbol's in-window candles as KindTrade ticks, then a post-15:30
// trade to freeze the settlement VWAP (exactly as the prod pipeline does).
func replay(eng *funding.Engine, sym string, candles [][]interface{}, today string) {
	var lastClose float64
	for _, c := range candles {
		hhmm := c[0].(string)
		close := c[1].(float64)
		vol := c[2].(float64)
		lastClose = close
		eng.Ingest(source.Tick{Symbol: sym, Price: close, Volume: vol, Kind: source.KindTrade, Timestamp: tsAt(today, hhmm)})
	}
	// Пост-15:30 сделка — замораживает settlVWAP по накопленному окну.
	eng.Ingest(source.Tick{Symbol: sym, Price: lastClose, Volume: 1, Kind: source.KindTrade, Timestamp: tsAt(today, "15:31")})
}

func tsAt(day, hhmm string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", day+" "+hhmm, msk)
	if err != nil {
		panic(err)
	}
	return t
}

func report(day, inst string, inf funding.InstrumentFunding, swap float64) float64 {
	if inf.CBFunding == nil {
		fmt.Printf("%-11s %-4s %10s %10s %10.5f %11s %s\n", day, inst, sv(inf.SettlVWAP), "nil", swap, "-", "❌ CBFunding=nil")
		return 0
	}
	cb := *inf.CBFunding
	diff := cb - swap
	verdict := "✅"
	if math.Abs(diff) > 0.004 {
		verdict = "⚠️ >0.004"
	}
	fmt.Printf("%-11s %-4s %10s %10.5f %10.5f %+11.5f %s\n", day, inst, sv(inf.SettlVWAP), cb, swap, diff, verdict)
	return math.Abs(diff)
}

func sv(p *float64) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprintf("%.4f", *p)
}
