package robots

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

var base = time.Date(2026, 8, 17, 11, 0, 0, 0, time.FixedZone("MSK", 3*60*60))

// truncSec повторяет то, что делает биржевая лента: TRADETIME приходит с
// разрешением в одну секунду, доли секунды до нас не доезжают.
func truncSec(t time.Time) time.Time { return t.Truncate(time.Second) }

// robotPrints строит серию робота: period между принтами, лотовка гуляет в
// пределах qtyJitter, время огрублено до секунды.
func robotPrints(sym string, side Side, start time.Time, period float64, n int, qty, qtyJitter float64, rnd *rand.Rand) []Print {
	out := make([]Print, 0, n)
	for i := 0; i < n; i++ {
		ts := start.Add(time.Duration(float64(i) * period * float64(time.Second)))
		out = append(out, Print{
			TradeNo: int64(1_000_000 + i),
			Symbol:  sym,
			Time:    truncSec(ts),
			Price:   100 + rnd.Float64(),
			Qty:     qty + math.Round(rnd.Float64()*qtyJitter),
			Side:    side,
		})
	}
	return out
}

// noisePrints — обычная лента: случайные моменты, случайные размеры.
func noisePrints(sym string, start time.Time, n int, rnd *rand.Rand) []Print {
	out := make([]Print, 0, n)
	ts := start
	for i := 0; i < n; i++ {
		ts = ts.Add(time.Duration(rnd.Float64() * 3 * float64(time.Second)))
		side := SideBuy
		if rnd.Intn(2) == 0 {
			side = SideSell
		}
		out = append(out, Print{
			TradeNo: int64(2_000_000 + i),
			Symbol:  sym,
			Time:    truncSec(ts),
			Price:   100 + rnd.Float64(),
			Qty:     float64(1 + rnd.Intn(5000)),
			Side:    side,
		})
	}
	return out
}

// Основной сценарий из постановки: принт раз в 11.2 с размером 3011–3013 лотов,
// закопанный в обычную ленту. Детектор обязан вытащить и тайминг, и лотовку,
// и направление — несмотря на то, что метки времени огрублены до секунды.
func TestDetectPeriodicRobotInNoise(t *testing.T) {
	rnd := rand.New(rand.NewSource(42))
	d := NewDetector(DefaultConfig())

	d.Add(noisePrints("SBER", base, 600, rnd)...)
	d.Add(robotPrints("SBER", SideBuy, base.Add(30*time.Second), 11.2, 60, 3011, 2, rnd)...)

	found := d.Scan(base.Add(20 * time.Minute))
	if len(found) == 0 {
		t.Fatal("робот не найден в ленте")
	}

	var r *Robot
	for i := range found {
		if math.Abs(found[i].PeriodSec-11.2) < 0.5 {
			r = &found[i]
			break
		}
	}
	if r == nil {
		t.Fatalf("нет находки с периодом ~11.2 с; найдено: %+v", found)
	}
	if r.Side != SideBuy {
		t.Errorf("Side = %q, хотим %q", r.Side, SideBuy)
	}
	if math.Abs(r.PeriodSec-11.2) > 0.15 {
		t.Errorf("PeriodSec = %.3f, хотим 11.2 ± 0.15", r.PeriodSec)
	}
	if r.QtyMin < 3011 || r.QtyMax > 3013 {
		t.Errorf("лотовка [%.0f, %.0f], хотим внутри [3011, 3013]", r.QtyMin, r.QtyMax)
	}
	if r.Prints < 55 {
		t.Errorf("Prints = %d, хотим почти все 60 принтов серии", r.Prints)
	}
	if r.Confidence < 0.5 {
		t.Errorf("Confidence = %.2f, для чистой серии хотим > 0.5", r.Confidence)
	}
}

// Точность периода не должна упираться в секундную сетку биржевых меток:
// период считается как Σинтервалов / Σтактов, и на длинной серии восстанавливается
// с точностью до сотых, хотя каждый отдельный интервал приходит целым числом секунд.
func TestPeriodPrecisionBeatsSecondGranularity(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	for _, want := range []float64{4.3, 11.2, 27.75} {
		d := NewDetector(DefaultConfig())
		d.Add(robotPrints("GAZP", SideSell, base, want, 80, 500, 0, rnd)...)
		found := d.Scan(base.Add(20 * time.Minute))
		if len(found) != 1 {
			t.Fatalf("период %.2f: найдено %d роботов, хотим 1", want, len(found))
		}
		if got := found[0].PeriodSec; math.Abs(got-want) > 0.05 {
			t.Errorf("PeriodSec = %.3f, хотим %.2f ± 0.05", got, want)
		}
		if found[0].Side != SideSell {
			t.Errorf("Side = %q, хотим %q", found[0].Side, SideSell)
		}
	}
}

// Робот, пропускающий такты (не нашёл ликвидности), остаётся одним роботом —
// период не должен «растягиваться» на пропуски.
func TestRobotWithSkippedBeats(t *testing.T) {
	rnd := rand.New(rand.NewSource(3))
	full := robotPrints("LKOH", SideBuy, base, 15, 60, 200, 0, rnd)
	var kept []Print
	for i, p := range full {
		if i%5 == 3 {
			continue // каждый пятый такт робот молчит
		}
		kept = append(kept, p)
	}

	d := NewDetector(DefaultConfig())
	d.Add(kept...)
	found := d.Scan(base.Add(20 * time.Minute))
	if len(found) != 1 {
		t.Fatalf("найдено %d роботов, хотим 1: %+v", len(found), found)
	}
	if got := found[0].PeriodSec; math.Abs(got-15) > 0.2 {
		t.Errorf("PeriodSec = %.3f, хотим 15 ± 0.2", got)
	}
}

// Обычная лента без роботов не должна давать находок вовсе.
//
// Порог в ConfidentPrints принтов держится именно на этом: серию такой длины
// случайные сделки одного размера не выстраивают. Стоит опустить порог — и
// совпадения начинают проходить (см. TestCurrencyThresholdCostOnRandomTape).
func TestNoFalsePositivesOnRandomTape(t *testing.T) {
	for seed := int64(0); seed < 20; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		d := NewDetector(DefaultConfig())
		d.Add(noisePrints("MOEX", base, 900, rnd)...)
		if found := d.Scan(base.Add(20 * time.Minute)); len(found) != 0 {
			t.Errorf("seed %d: на случайной ленте найдено %d роботов: %+v", seed, len(found), found)
		}
	}
}

// Цена валютного исключения, зафиксированная числом.
//
// На валюте робот заявляется с третьего повторяющегося принта, и на такой длине
// совпадения ещё возможны: три случайные сделки одного размера иногда встают через
// равные промежутки. Тест не требует нуля — он сторожит, что такие находки остаются
// короткими и помеченными предварительными, а не выдаются за роботов. Со страницы
// их снимает правило пропущенных тактов, на первом же несостоявшемся такте.
//
// Порядок величины важен: на пороге в два принта тот же замер давал 1047 ложных
// находок, на трёх — единицы. Если это число вдруг вырастет, порог поехал.
func TestCurrencyThresholdCostOnRandomTape(t *testing.T) {
	var provisional, confirmed int
	for seed := int64(0); seed < 20; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		d := NewDetector(DefaultConfig())
		d.MarkCurrency("CNYRUBF")
		d.Add(noisePrints("CNYRUBF", base, 900, rnd)...)
		for _, r := range d.Scan(base.Add(20 * time.Minute)) {
			if r.Provisional {
				provisional++
			} else {
				confirmed++
			}
		}
	}
	if confirmed != 0 {
		t.Errorf("на случайной ленте %d подтверждённых роботов — так быть не должно", confirmed)
	}
	if provisional > 20 {
		t.Errorf("предварительных находок на случайных лентах %d — порог обнаружения поехал", provisional)
	}
	t.Logf("предварительных находок на 20 случайных лентах валюты: %d", provisional)
}

// Плотная лента однолотовых сделок не робот: сделки идут часто, но неравномерно.
func TestDenseSingleLotTapeIsNotRobot(t *testing.T) {
	rnd := rand.New(rand.NewSource(11))
	d := NewDetector(DefaultConfig())
	ts := base
	for i := 0; i < 800; i++ {
		ts = ts.Add(time.Duration(rnd.ExpFloat64() * 4 * float64(time.Second)))
		d.Add(Print{TradeNo: int64(i), Symbol: "VTBR", Time: truncSec(ts), Price: 100, Qty: 1, Side: SideBuy})
	}
	if found := d.Scan(base.Add(60 * time.Minute)); len(found) != 0 {
		t.Errorf("однолотовая лента распознана как робот: %+v", found)
	}
}

// Два робота на одном тикере в разные стороны — две отдельные строки.
func TestTwoRobotsOppositeSides(t *testing.T) {
	rnd := rand.New(rand.NewSource(5))
	d := NewDetector(DefaultConfig())
	d.Add(robotPrints("ROSN", SideBuy, base, 9, 70, 300, 0, rnd)...)
	d.Add(robotPrints("ROSN", SideSell, base.Add(2*time.Second), 21.5, 40, 1200, 0, rnd)...)

	found := d.Scan(base.Add(20 * time.Minute))
	if len(found) != 2 {
		t.Fatalf("найдено %d роботов, хотим 2: %+v", len(found), found)
	}
	bySide := map[Side]Robot{}
	for _, r := range found {
		bySide[r.Side] = r
	}
	if got := bySide[SideBuy].PeriodSec; math.Abs(got-9) > 0.2 {
		t.Errorf("лонг: PeriodSec = %.2f, хотим 9", got)
	}
	if got := bySide[SideSell].PeriodSec; math.Abs(got-21.5) > 0.3 {
		t.Errorf("шорт: PeriodSec = %.2f, хотим 21.5", got)
	}
	if got := bySide[SideSell].QtyTypical; got != 1200 {
		t.Errorf("шорт: QtyTypical = %.0f, хотим 1200", got)
	}
}

// Окно анализа скользит: принты старше Window выпадают из ленты.
func TestTrimDropsOldPrints(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	d := NewDetector(DefaultConfig())
	d.Add(robotPrints("SBER", SideBuy, base, 10, 50, 100, 0, rnd)...)
	if n := d.TapeLen("SBER"); n != 50 {
		t.Fatalf("TapeLen = %d, хотим 50", n)
	}
	d.Trim(base.Add(2 * time.Hour))
	if n := d.TapeLen("SBER"); n != 0 {
		t.Errorf("после Trim осталось %d принтов, хотим 0", n)
	}
}

func TestSameRobotMatching(t *testing.T) {
	a := Robot{Symbol: "SBER", Side: SideBuy, QtyMin: 3011, QtyMax: 3013, PeriodSec: 11.2}
	tests := []struct {
		name string
		b    Robot
		want bool
	}{
		{"та же серия чуть позже", Robot{Symbol: "SBER", Side: SideBuy, QtyMin: 3012, QtyMax: 3014, PeriodSec: 11.35}, true},
		{"другой тикер", Robot{Symbol: "GAZP", Side: SideBuy, QtyMin: 3011, QtyMax: 3013, PeriodSec: 11.2}, false},
		{"другое направление", Robot{Symbol: "SBER", Side: SideSell, QtyMin: 3011, QtyMax: 3013, PeriodSec: 11.2}, false},
		{"другая лотовка", Robot{Symbol: "SBER", Side: SideBuy, QtyMin: 500, QtyMax: 502, PeriodSec: 11.2}, false},
		{"другой период", Robot{Symbol: "SBER", Side: SideBuy, QtyMin: 3011, QtyMax: 3013, PeriodSec: 20}, false},
	}
	for _, tt := range tests {
		if got := SameRobot(a, tt.b); got != tt.want {
			t.Errorf("%s: SameRobot = %v, хотим %v", tt.name, got, tt.want)
		}
	}
}
