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
//	go run ./cmd/simraw <trades.json> <symbol> <prev_settle> <cb_rate_new> <swaprate_факт> [опережение_marketdata_сек] [отставание_ленты_ISS_сек]
//
// Шестой (необязательный) аргумент моделирует ГЛАВНУЮ граблю прод-пути: поток
// marketdata бежит впереди потока сделок. Публичный фид ISS отдаёт данные с
// задержкой ~15 минут, и пока тик marketdata штамповался временем ответа сервера
// (SYSTIME), он опережал сделки ровно на эту задержку — движок морозил ногу
// фьючерса окном, обрезанным на ~15:15 (28.07.2026: USD −0.06770 вместо −0.06545).
// Прогон с опережением 900 секунд обязан давать тот же ответ, что и без него.
//
// Седьмой аргумент включает ЖИВОЙ ПОТОК брокера и задаёт, на сколько от него
// отстаёт лента ISS. Это точная модель прода с 28.08.2026: одна и та же сделка
// приходит дважды — сперва живым потоком в своё биржевое время, потом лентой
// ISS на четверть часа позже. Прогон с отставанием 900 секунд обязан дать ту же
// ногу, что и прогон без задержек вовсе: живой поток закрывает окно сам, а
// догнавшая лента только подтверждает результат. Именно это и не работало
// 19.08.2026, когда EUR ушёл подписчикам как 0.03367 вместо 0.02919.
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
	if len(os.Args) < 6 || len(os.Args) > 8 {
		fmt.Println("usage: simraw <trades.json> <symbol> <prev_settle> <cb_rate_new> <swaprate> [marketdata_lead_sec] [iss_lag_sec]")
		os.Exit(2)
	}
	path, sym := os.Args[1], os.Args[2]
	prevSettle, cbNew, swap := mustF(os.Args[3]), mustF(os.Args[4]), mustF(os.Args[5])
	var mdLead, issLag time.Duration
	if len(os.Args) >= 7 {
		mdLead = time.Duration(mustF(os.Args[6])) * time.Second
	}
	if len(os.Args) == 8 {
		issLag = time.Duration(mustF(os.Args[7])) * time.Second
	}

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

	// Живой поток поднимается до открытия окна — иначе движок им не воспользуется:
	// подписка, начатая среди дня, утренних сделок уже не увидит.
	if issLag > 0 {
		eng.Ingest(source.Tick{Symbol: sym, Kind: source.KindStreamUp, Live: true,
			Timestamp: at(today, "09:00:00")})
	}

	// deferred — сделки, ещё не доехавшие лентой ISS. В режиме живого потока одна
	// и та же сделка приходит дважды: сразу потоком и через issLag лентой.
	type pending struct {
		tick source.Tick
		vol  float64
	}
	var deferred []pending
	flushISS := func(upto time.Time) {
		i := 0
		for ; i < len(deferred); i++ {
			d := deferred[i]
			if d.tick.Timestamp.After(upto) {
				break
			}
			if mdLead > 0 {
				eng.Ingest(source.Tick{Symbol: sym, Price: d.tick.Price, Volume: d.vol,
					Kind: source.KindLastPrice, Timestamp: d.tick.Timestamp.Add(mdLead)})
			}
			eng.Ingest(d.tick)
		}
		deferred = deferred[i:]
	}

	var backdated, used int
	var lastTS time.Time
	var dayVol float64
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

		issTick := source.Tick{Symbol: sym, Price: price, Volume: qty,
			Kind: source.KindTrade, Timestamp: ts, Backdated: isBack}
		dayVol += qty

		if issLag > 0 {
			// Живой поток: сделка приходит в своё биржевое время, без задержки.
			eng.Ingest(source.Tick{Symbol: sym, Price: price, Volume: qty,
				Kind: source.KindTrade, Timestamp: ts, Backdated: isBack, Live: true})
			// А лента ISS довозит её на issLag позже — вместе со всем, что успело
			// «дозреть» к этому моменту.
			deferred = append(deferred, pending{tick: issTick, vol: dayVol})
			flushISS(ts.Add(-issLag))
			lastTS = ts
			continue
		}

		// Тик marketdata, опережающий поток сделок: VOLTODAY уже включает эту сделку,
		// а сама она движку ещё не приехала — ровно то, что делает боевой фид.
		if mdLead > 0 {
			eng.Ingest(source.Tick{Symbol: sym, Price: price, Volume: dayVol,
				Kind: source.KindLastPrice, Timestamp: ts.Add(mdLead)})
		}

		eng.Ingest(issTick)
	}

	// Хвост ленты ISS: к моменту публикации курса ЦБ (17:07) она давно догнала.
	snapBeforeISS := eng.Snapshot()
	flushISS(at(today, "23:59:59"))
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
	if issLag > 0 {
		before := snapBeforeISS.USDRUBF
		if sym == source.SymbolEURRUBF {
			before = snapBeforeISS.EURRUBF
		}
		fmt.Printf("  режим: живой поток + лента ISS с отставанием %s\n", issLag)
		fmt.Printf("  нога ДО прихода хвоста ISS = %s (источник %q, предварительно=%v)\n",
			f(before.SettlVWAP), before.SettlSource, before.SettlProvisional)
	}
	fmt.Printf("  PREVSETTLE=%.2f  курс ЦБ (новый)=%.4f\n", prevSettle, cbNew)
	fmt.Printf("  settlVWAP движка = %s\n", f(inf.SettlVWAP))
	fmt.Printf("  источник ноги    = %q (предварительно=%v)\n", inf.SettlSource, inf.SettlProvisional)
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
