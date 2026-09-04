package robots

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

// rangedRobotPrints — робот, который держит ровным только такт: объём каждый раз
// свой, в пределах [lo, hi]. Время миллисекундное, как в потоке брокера.
func rangedRobotPrints(sym string, side Side, start time.Time, period float64, n int, lo, hi float64, rnd *rand.Rand) []Print {
	out := make([]Print, 0, n)
	for i := 0; i < n; i++ {
		ts := start.Add(time.Duration(float64(i) * period * float64(time.Second)))
		ts = ts.Add(time.Duration(rnd.NormFloat64() * 40 * float64(time.Millisecond)))
		out = append(out, Print{
			TradeNo: int64(3_000_000 + i),
			Symbol:  sym,
			Time:    ts,
			Price:   100 + rnd.Float64(),
			Qty:     lo + math.Floor(rnd.Float64()*(hi-lo+1)),
			Side:    side,
		})
	}
	return out
}

// msNoisePrints — обычная лента с миллисекундной меткой: случайные моменты,
// случайные размеры, gap — средний промежуток между сделками.
func msNoisePrints(sym string, start time.Time, n int, gap float64, rnd *rand.Rand) []Print {
	out := make([]Print, 0, n)
	ts := start
	for i := 0; i < n; i++ {
		ts = ts.Add(time.Duration(rnd.Float64() * 2 * gap * float64(time.Second)))
		side := SideBuy
		if rnd.Intn(2) == 0 {
			side = SideSell
		}
		out = append(out, Print{
			TradeNo: int64(4_000_000 + i),
			Symbol:  sym,
			Time:    ts,
			Price:   100 + rnd.Float64(),
			Qty:     float64(1 + rnd.Intn(500)),
			Side:    side,
		})
	}
	return out
}

func rangedFinds(found []Robot) []Robot {
	var out []Robot
	for _, r := range found {
		if r.Ranged {
			out = append(out, r)
		}
	}
	return out
}

// Робот бьёт каждые 12 с, а объём каждый раз новый — от 5 до 300 лотов. Разбор по
// кластерам лотовки такого не видит вовсе: в кластер «около 40 лотов» попадает
// один принт из полусотни. Найти его должен разбор всей стороны.
func TestDetectRangedRobot(t *testing.T) {
	rnd := rand.New(rand.NewSource(11))
	d := NewDetector(DefaultConfig())

	d.Add(msNoisePrints("MOEX", base, 400, 1.5, rnd)...)
	d.Add(rangedRobotPrints("MOEX", SideBuy, base.Add(20*time.Second), 12, 60, 5, 300, rnd)...)

	found := rangedFinds(d.Scan(base.Add(20 * time.Minute)))
	if len(found) == 0 {
		t.Fatal("диапазонный робот не найден")
	}

	var r *Robot
	for i := range found {
		if math.Abs(found[i].PeriodSec-12) < 0.6 {
			r = &found[i]
			break
		}
	}
	if r == nil {
		t.Fatalf("нет находки с тактом ~12 с; найдено: %+v", found)
	}
	if r.Side != SideBuy {
		t.Errorf("Side = %q, хотим %q", r.Side, SideBuy)
	}
	if math.Abs(r.PeriodSec-12) > 0.2 {
		t.Errorf("PeriodSec = %.3f, хотим 12 ± 0.2", r.PeriodSec)
	}
	// Смысл находки в том, что лотовка гуляет: диапазон обязан быть широким,
	// иначе робота нашёл бы и обычный разбор.
	if r.QtyMax-r.QtyMin < 100 {
		t.Errorf("лотовка [%.0f, %.0f], у диапазонного робота ждём широкий размах", r.QtyMin, r.QtyMax)
	}
	if r.Hits < 30 {
		t.Errorf("Hits = %d, хотим большую часть из 60 ударов серии", r.Hits)
	}
	if r.Provisional {
		t.Error("диапазонная находка помечена предварительной: короче подтверждённой серии её заявлять нельзя")
	}
}

// Обычная лента без робота не должна рождать диапазонных находок ни при какой
// плотности. Свидетельства лотовки у них нет, и вся защита — в оценке вероятности
// совпадения; проверять её надо перебором, а не одной удачной затравкой.
func TestRangedFindsNothingInNoise(t *testing.T) {
	for _, gap := range []float64{0.4, 0.8, 1.2, 2, 4, 8} {
		for seed := int64(1); seed <= 20; seed++ {
			rnd := rand.New(rand.NewSource(seed * 7919))
			d := NewDetector(DefaultConfig())
			d.Add(msNoisePrints("LKOH", base, int(1800/gap), gap, rnd)...)
			found := rangedFinds(d.Scan(base.Add(35 * time.Minute)))
			if len(found) != 0 {
				t.Errorf("промежуток %.1f с, затравка %d: на случайной ленте найдено %d диапазонных роботов: %+v",
					gap, seed, len(found), found)
			}
		}
	}
}

// Обратная сторона той же монеты: на ленте, где робот различим, его обязаны найти.
//
// Различимость здесь не на глаз, а по той же арифметике, что стоит в gridChance:
// сетка такта ловит чужие сделки сама собой тем чаще, чем плотнее лента, и с
// плотности около двух сделок в секунду диапазонного робота не отличить от узора
// уже никаким порогом. Ниже неё он обязан находиться — по всем тактам от шести
// секунд до минуты.
func TestRangedRecall(t *testing.T) {
	if testing.Short() {
		t.Skip("перебор по тактам и плотностям")
	}
	for _, period := range []float64{6, 12, 30, 60} {
		for _, gap := range []float64{1.5, 4} {
			miss := 0
			for seed := int64(1); seed <= 10; seed++ {
				rnd := rand.New(rand.NewSource(seed * 104729))
				d := NewDetector(DefaultConfig())
				d.Add(msNoisePrints("MOEX", base, int(1800/gap), gap, rnd)...)
				d.Add(rangedRobotPrints("MOEX", SideBuy, base.Add(30*time.Second),
					period, int(1500/period), 5, 300, rnd)...)

				found := false
				for _, r := range rangedFinds(d.Scan(base.Add(35 * time.Minute))) {
					if r.Side == SideBuy && math.Abs(r.PeriodSec-period) < 0.3 {
						found = true
					}
				}
				if !found {
					miss++
				}
			}
			// Такт в минуту на ленте в две трети сделки в секунду — край
			// различимости: ударов там всего два десятка, и часть прогонов
			// теряется. Остальные сочетания обязаны находиться всегда.
			limit := 0
			if period == 60 && gap == 1.5 {
				limit = 4
			}
			if miss > limit {
				t.Errorf("такт %.0f с, промежуток %.1f с: не найден в %d прогонах из 10 (допустимо %d)",
					period, gap, miss, limit)
			}
		}
	}
}

// Робот с постоянной лотовкой обязан остаться одной строкой: разбор всей стороны
// нашёл бы его вторично — уже «диапазонным», с диапазоном в один лот.
func TestSteadyRobotIsNotReportedTwice(t *testing.T) {
	rnd := rand.New(rand.NewSource(3))
	d := NewDetector(DefaultConfig())

	d.Add(msNoisePrints("GAZP", base, 300, 1.5, rnd)...)
	d.Add(rangedRobotPrints("GAZP", SideSell, base.Add(10*time.Second), 15, 50, 371, 371, rnd)...)

	found := d.Scan(base.Add(20 * time.Minute))
	var steady, ranged int
	for _, r := range found {
		if math.Abs(r.PeriodSec-15) > 1 {
			continue
		}
		if r.Ranged {
			ranged++
		} else {
			steady++
		}
	}
	if steady != 1 {
		t.Errorf("робот с ровной лотовкой найден %d раз, хотим 1", steady)
	}
	if ranged != 0 {
		t.Errorf("тот же робот заявлен ещё и диапазонным %d раз", ranged)
	}
}

// Плотную сторону разбирать бессмысленно: сетка такта там ловит чужие сделки сама
// собой, и ни одна гипотеза проверку на случайность не пройдёт. Такую сторону
// диапазонный проход не трогает вовсе — это и защита от ложных находок, и
// экономия процессора на самых тяжёлых бумагах.
func TestRangeScanSkipsDenseSide(t *testing.T) {
	d := NewDetector(DefaultConfig())
	th := d.thresholdsFor("SBER")

	sparse := make([]Print, 0, 60)
	for i := 0; i < 60; i++ {
		sparse = append(sparse, Print{Symbol: "SBER", Time: base.Add(time.Duration(i) * 10 * time.Second), Qty: 1, Side: SideBuy})
	}
	if !d.rangeWorthScanning(sparse, th) {
		t.Error("редкая сторона не разбирается, хотя сетка на ней различима")
	}

	dense := make([]Print, 0, 6000)
	for i := 0; i < 6000; i++ {
		dense = append(dense, Print{Symbol: "SBER", Time: base.Add(time.Duration(i) * 100 * time.Millisecond), Qty: 1, Side: SideBuy})
	}
	if d.rangeWorthScanning(dense, th) {
		t.Error("плотная сторона пошла в разбор: на ней сетку от совпадения не отличить")
	}
}
