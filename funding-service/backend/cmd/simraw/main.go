// Command simraw прогоняет СЫРЫЕ сделки ISS (все, не свечи) через боевой движок
// фандинга и сравнивает результат с фактическим SWAPRATE биржи.
//
// В отличие от cmd/sim (который кормит движок минутными свечами и потому годится
// только для грубой проверки) здесь воспроизводится ровно прод-путь: каждая сделка
// из trades.json = один KindTrade-тик, включая сделки чужих дней, приписанные к
// сессии (TRADEDATE != TRADE_SESSION_DATE) — их движок обязан отбросить из окна.
//
// Файлы сделок готовит scripts/capture_funding_vwap.py (scripts/out/trades_*.json).
//
// Запуск:
//
//	go run ./cmd/simraw <trades.json> <symbol> <prev_settle> <cb_rate_new> <swaprate_факт>
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/source"
)

var msk = time.FixedZone("MSK", 3*60*60)

type table struct {
	Columns []string        `json:"columns"`
	Data    [][]interface{} `json:"data"`
}

func main() {
	if len(os.Args) != 6 {
		fmt.Println("usage: simraw <trades.json> <symbol> <prev_settle> <cb_rate_new> <swaprate>")
		os.Exit(2)
	}
	path, sym := os.Args[1], os.Args[2]
	prevSettle, cbNew, swap := mustF(os.Args[3]), mustF(os.Args[4]), mustF(os.Args[5])

	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	var t table
	if err := json.Unmarshal(b, &t); err != nil {
		panic(err)
	}
	idx := map[string]int{}
	for i, c := range t.Columns {
		idx[c] = i
	}

	// Дата сессии в файле → сегодняшняя МСК-дата: гейты движка смотрят на time.Now().
	today := time.Now().In(msk).Format("2006-01-02")
	officialSym := source.SymbolUSDRubOfficial
	if sym == source.SymbolEURRUBF {
		officialSym = source.SymbolEURRubOfficial
	}

	eng := funding.NewEngine()
	eng.Ingest(source.Tick{Symbol: sym, Price: prevSettle, Kind: source.KindPrevSettle,
		Timestamp: at(today, "09:00:00")})

	var backdated, used int
	var lastTS time.Time
	for _, row := range t.Data {
		cell := func(name string) interface{} { return row[idx[name]] }
		price, _ := cell("PRICE").(float64)
		qty, _ := cell("QUANTITY").(float64)
		if price <= 0 || qty <= 0 {
			continue
		}
		if off, ok := cell("OFFMARKETDEAL").(float64); ok && off != 0 {
			continue
		}
		tradeDate, _ := cell("TRADEDATE").(string)
		sessDate, _ := cell("TRADE_SESSION_DATE").(string)
		isBack := tradeDate != "" && sessDate != "" && tradeDate != sessDate
		clock, _ := cell("TRADETIME").(string)
		if isBack {
			// Как в клиенте: такая сделка живёт в моменте SYSTIME (утренний клиринг).
			if sys, ok := cell("SYSTIME").(string); ok && len(sys) >= 19 {
				clock = sys[11:]
			}
			backdated++
		} else {
			used++
		}
		ts := at(today, clock)
		lastTS = ts
		eng.Ingest(source.Tick{Symbol: sym, Price: price, Volume: qty,
			Kind: source.KindTrade, Timestamp: ts, Backdated: isBack})
	}
	_ = lastTS

	// Публикация курса ЦБ — момент фиксации фандинга.
	eng.Ingest(source.Tick{Symbol: officialSym, Price: cbNew,
		Kind: source.KindNewOfficialRate, Timestamp: at(today, "17:07:00")})

	snap := eng.Snapshot()
	inf := snap.USDRUBF
	if sym == source.SymbolEURRUBF {
		inf = snap.EURRUBF
	}

	fmt.Printf("%s: сделок в потоке %d (из них чужих дней %d)\n", sym, used+backdated, backdated)
	fmt.Printf("  PREVSETTLE=%.2f  курс ЦБ (новый)=%.4f\n", prevSettle, cbNew)
	fmt.Printf("  settlVWAP движка = %s\n", f(inf.SettlVWAP))
	fmt.Printf("  VWAP на витрине  = %.5f (после 15:30 = замороженная нога)\n", inf.VWAP)
	fmt.Printf("  CBFunding движка = %s\n", f(inf.CBFunding))
	fmt.Printf("  SWAPRATE биржи   = %.5f\n", swap)
	if inf.CBFunding != nil {
		fmt.Printf("  разница          = %+.5f\n", *inf.CBFunding-swap)
	}
	if inf.PredictedFunding != nil {
		fmt.Printf("  ВНИМАНИЕ: прогнозный фандинг после 15:30 должен быть пустым, got %.5f\n", *inf.PredictedFunding)
	}
}

func at(day, clock string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", day+" "+clock, msk)
	if err != nil {
		panic(err)
	}
	return t
}

func mustF(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		panic(err)
	}
	return v
}

func f(p *float64) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprintf("%.5f", *p)
}
