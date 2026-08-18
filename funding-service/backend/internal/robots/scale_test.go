package robots

import (
	"math/rand"
	"testing"
	"time"
)

// marketTape собирает ленту, похожую на весь рынок акций: измерено на живом ISS
// 18.08.2026 — 83 сделки в секунду, 150 тикеров в минуту, поток резко перекошен
// (на самый плотный тикер приходится четверть всех сделок).
func marketTape(symbols, tradesPerSec int, window time.Duration, rnd *rand.Rand) []Print {
	names := make([]string, symbols)
	weight := make([]float64, symbols)
	var total float64
	for i := range names {
		names[i] = string(rune('A'+i%26)) + string(rune('A'+(i/26)%26)) + string(rune('A'+(i/676)%26)) + "X"
		// Зипфоподобный перекос: первые тикеры собирают основную долю ленты.
		weight[i] = 1 / float64(i+1)
		total += weight[i]
	}

	n := int(window.Seconds()) * tradesPerSec
	out := make([]Print, 0, n)
	start := base
	for i := 0; i < n; i++ {
		r := rnd.Float64() * total
		var sym int
		for acc := 0.0; sym < symbols-1; sym++ {
			acc += weight[sym]
			if acc >= r {
				break
			}
		}
		side := SideBuy
		if rnd.Intn(2) == 0 {
			side = SideSell
		}
		out = append(out, Print{
			TradeNo: int64(i + 1),
			Symbol:  names[sym],
			// Метка биржи дискретна по секунде — как в настоящей ленте.
			Time:  start.Add(time.Duration(float64(i) / float64(tradesPerSec) * float64(time.Second))).Truncate(time.Second),
			Price: 100,
			Qty:   float64(1 + rnd.Intn(500)),
			Side:  side,
		})
	}
	return out
}

// Скан всего рынка должен укладываться в интервал сканирования с большим запасом:
// иначе перевод сбора на ленту рынка целиком упрётся в процессор, а не в сеть.
//
// Замер нужен именно на перекошенном потоке: тяжёлый тикер даёт в окне десятки
// тысяч принтов, а разбор кластеров по лотовке — не линейный.
func TestScanAtMarketScale(t *testing.T) {
	if testing.Short() {
		t.Skip("нагрузочный замер")
	}
	rnd := rand.New(rand.NewSource(7))
	cfg := DefaultConfig()
	tape := marketTape(150, 83, cfg.Window, rnd)

	d := NewDetector(cfg)
	addStart := time.Now()
	d.Add(tape...)
	addTook := time.Since(addStart)

	scanStart := time.Now()
	found := d.Scan(base.Add(cfg.Window))
	scanTook := time.Since(scanStart)

	var provisional, confirmed int
	for _, r := range found {
		if r.Provisional {
			provisional++
		} else {
			confirmed++
		}
	}
	t.Logf("принтов %d, тикеров %d: Add %s, Scan %s", len(tape), 150,
		addTook.Round(time.Millisecond), scanTook.Round(time.Millisecond))
	t.Logf("находок на случайной ленте: %d (предварительных %d, подтверждённых %d)",
		len(found), provisional, confirmed)

	// ScanInterval по умолчанию 15 с; берём десятикратный запас.
	if limit := 1500 * time.Millisecond; scanTook > limit {
		t.Errorf("скан рынка занял %s, предел %s", scanTook, limit)
	}
}
