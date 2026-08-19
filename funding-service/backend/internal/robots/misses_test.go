package robots

import (
	"math/rand"
	"testing"
	"time"
)

// Низкий порог обнаружения — привилегия валюты: там робот заявляется с третьего
// повторяющегося принта. На остальных инструментах серия должна дорасти до длины,
// на которой периодичность уже не объясняется совпадением: на ленте всего рынка
// короткие серии давали около сотни ложных находок одновременно.
func TestDetectionThresholdDependsOnInstrument(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))

	cases := []struct {
		name     string
		symbol   string
		currency bool
		prints   int
		want     bool
	}{
		{"валюта, три принта", "USDRUBF", true, 3, true},
		{"валюта, два принта", "USDRUBF", true, 2, false},
		{"акция, три принта", "SBER", false, 3, false},
		{"акция, пять принтов", "SBER", false, 5, false},
		{"акция, шесть принтов", "SBER", false, ConfidentPrints, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDetector(DefaultConfig())
			if tc.currency {
				d.MarkCurrency(tc.symbol)
			}
			d.Add(robotPrints(tc.symbol, SideBuy, base, 12, tc.prints, 400, 0, rnd)...)

			found := d.Scan(base.Add(10 * time.Minute))
			if got := len(found) > 0; got != tc.want {
				t.Fatalf("найдено %d роботов, хотим наличие=%v: %+v", len(found), tc.want, found)
			}
			if !tc.want {
				return
			}
			// Предварительной помечается только та серия, что короче уверенной
			// длины, — то есть на практике лишь валютная.
			wantProvisional := tc.prints < ConfidentPrints
			if got := found[0].Provisional; got != wantProvisional {
				t.Errorf("Provisional = %v, хотим %v при серии в %d принтов",
					got, wantProvisional, tc.prints)
			}
		})
	}
}

// Допуск лотовки двусторонний: серия 3011–3013 при ±1 лоте — один робот.
func TestQtyToleranceIsPlusMinusOneLot(t *testing.T) {
	d := NewDetector(DefaultConfig())
	for i := 0; i < 8; i++ {
		d.Add(Print{
			TradeNo: int64(i + 1),
			Symbol:  "SBER",
			Time:    truncSec(base.Add(time.Duration(i) * 10 * time.Second)),
			Price:   250,
			Qty:     3012 + float64(i%3-1), // 3011, 3012, 3013
			Side:    SideSell,
		})
	}
	found := d.Scan(base.Add(10 * time.Minute))
	if len(found) != 1 {
		t.Fatalf("найдено %d роботов, хотим 1: %+v", len(found), found)
	}
	if found[0].Prints != 8 {
		t.Errorf("Prints = %d, хотим 8: разброс ±1 лот не должен резать серию", found[0].Prints)
	}
}

// Разница в сорок лотов — уже разные роботы, а не один с широкой лотовкой.
func TestQtyToleranceSplitsDistantSizes(t *testing.T) {
	d := NewDetector(DefaultConfig())
	var no int64
	for i := 0; i < 8; i++ {
		no++
		d.Add(Print{
			TradeNo: no, Symbol: "SBER", Price: 250, Qty: 100,
			Time: truncSec(base.Add(time.Duration(i) * 10 * time.Second)), Side: SideBuy,
		})
		no++
		d.Add(Print{
			TradeNo: no, Symbol: "SBER", Price: 250, Qty: 140,
			Time: truncSec(base.Add(time.Duration(i)*10*time.Second + 3*time.Second)), Side: SideBuy,
		})
	}
	found := d.Scan(base.Add(10 * time.Minute))
	if len(found) != 2 {
		t.Fatalf("найдено %d роботов, хотим 2 (лотовки 100 и 140): %+v", len(found), found)
	}
}

// registryFixture — реестр с одним активным роботом и головой его ленты.
func registryFixture(t *testing.T) (*registry, *Session, Robot) {
	t.Helper()
	cfg := DefaultConfig()
	reg := newRegistry(3*time.Minute, time.Hour, cfg.Window, 100, func(p float64) time.Duration {
		return beatTol(cfg, p)
	})
	rb := Robot{
		Symbol: "SBER", Side: SideBuy,
		QtyMin: 100, QtyMax: 100, QtyTypical: 100,
		PeriodSec: 30, Prints: 8, Beats: 7, Confidence: 0.8,
		FirstSeen: base, LastSeen: base.Add(210 * time.Second),
	}
	reg.observe([]Robot{rb}, base.Add(4*time.Minute), map[string]time.Time{"SBER": rb.LastSeen})
	if len(reg.sessions) != 1 {
		t.Fatalf("в реестре %d сессий, хотим 1", len(reg.sessions))
	}
	return reg, reg.sessions[0], rb
}

// Пропущенный такт подсвечивается, второй подряд убирает робота со страницы.
func TestMissedBeatDropsRobotOnSecondMiss(t *testing.T) {
	reg, s, rb := registryFixture(t)
	if s.Misses != 0 {
		t.Fatalf("Misses = %d сразу после находки, хотим 0", s.Misses)
	}

	// Лента ушла на такт вперёд, а робот не напечатал — первый пропуск.
	head := rb.LastSeen.Add(32 * time.Second)
	reg.observe(nil, base.Add(5*time.Minute), map[string]time.Time{"SBER": head})
	if len(reg.sessions) != 1 {
		t.Fatalf("после первого пропуска сессий %d, хотим 1", len(reg.sessions))
	}
	if got := reg.sessions[0].Misses; got != 1 {
		t.Fatalf("Misses = %d после первого пропуска, хотим 1", got)
	}

	// Второй такт подряд мимо — робота снимаем.
	head = rb.LastSeen.Add(62 * time.Second)
	reg.observe(nil, base.Add(6*time.Minute), map[string]time.Time{"SBER": head})
	if len(reg.sessions) != 0 {
		t.Fatalf("после второго пропуска сессий %d, хотим 0: %+v", len(reg.sessions), reg.sessions)
	}

	// Но в базу закрытая строка уйти обязана.
	dirty := reg.takeDirty()
	if len(dirty) != 1 {
		t.Fatalf("к записи готово %d строк, хотим 1", len(dirty))
	}
	if dirty[0].Active {
		t.Error("снятая сессия должна закрыться в базе, а не остаться активной")
	}
}

// Робот, отработавший такт после пропуска, снова считается чистым.
func TestMissCounterResetsOnNewPrint(t *testing.T) {
	reg, _, rb := registryFixture(t)

	reg.observe(nil, base.Add(5*time.Minute), map[string]time.Time{"SBER": rb.LastSeen.Add(32 * time.Second)})
	if got := reg.sessions[0].Misses; got != 1 {
		t.Fatalf("Misses = %d, хотим 1", got)
	}

	printed := rb
	printed.LastSeen = rb.LastSeen.Add(60 * time.Second)
	printed.Prints = rb.Prints + 1
	reg.observe([]Robot{printed}, base.Add(6*time.Minute), map[string]time.Time{"SBER": printed.LastSeen})

	if len(reg.sessions) != 1 {
		t.Fatalf("сессий %d, хотим 1", len(reg.sessions))
	}
	if got := reg.sessions[0].Misses; got != 0 {
		t.Errorf("Misses = %d после нового принта, хотим 0", got)
	}
}

// Снятую серию нельзя тут же завести заново: она ещё лежит в окне анализа.
func TestDroppedRobotIsNotRecreated(t *testing.T) {
	reg, _, rb := registryFixture(t)

	reg.observe(nil, base.Add(5*time.Minute), map[string]time.Time{"SBER": rb.LastSeen.Add(32 * time.Second)})
	reg.observe(nil, base.Add(6*time.Minute), map[string]time.Time{"SBER": rb.LastSeen.Add(62 * time.Second)})
	if len(reg.sessions) != 0 {
		t.Fatalf("робот должен быть снят, сессий %d", len(reg.sessions))
	}

	// Детектор всё ещё видит ту же серию в ленте и приносит её снова.
	reg.observe([]Robot{rb}, base.Add(6*time.Minute+15*time.Second), map[string]time.Time{"SBER": rb.LastSeen.Add(62 * time.Second)})
	if len(reg.sessions) != 0 {
		t.Errorf("снятая серия заведена заново: %+v", reg.sessions)
	}

	// А когда серия уедет из окна анализа, чёрный список её отпускает.
	later := base.Add(6*time.Minute + DefaultConfig().Window + time.Minute)
	reg.observe([]Robot{rb}, later, map[string]time.Time{"SBER": rb.LastSeen})
	if len(reg.sessions) != 1 {
		t.Errorf("после истечения чёрного списка сессий %d, хотим 1", len(reg.sessions))
	}
}

// Время следующего удара продолжает фазу вперёд: лента запаздывает, и «последний
// принт плюс период» давно в прошлом.
func TestNextBeatExtrapolatesForward(t *testing.T) {
	r := Robot{PeriodSec: 30, LastSeen: base}

	if got := r.NextBeatAfter(base.Add(5 * time.Second)); !got.Equal(base.Add(30 * time.Second)) {
		t.Errorf("NextBeatAfter = %v, хотим %v", got, base.Add(30*time.Second))
	}
	// Прошло десять минут ленты — ждём ближайший такт впереди, а не base+30с.
	got := r.NextBeatAfter(base.Add(10 * time.Minute))
	if want := base.Add(10*time.Minute + 30*time.Second); !got.Equal(want) {
		t.Errorf("NextBeatAfter через 10 минут = %v, хотим %v", got, want)
	}
	if got := (Robot{}).NextBeatAfter(base); !got.IsZero() {
		t.Errorf("без периода такта быть не может, получили %v", got)
	}
}

// Сила робота — один его принт в доле часового оборота бумаги.
func TestSessionStrength(t *testing.T) {
	s := Session{Robot: Robot{
		Symbol: "SBER", Side: SideSell, QtyTypical: 100, Prints: 20, PeriodSec: 30, LastSeen: base,
	}}
	day := DayVolume{Symbol: "SBER", Date: base.Format("2006-01-02"), Buy: 100000, Sell: 40000}
	s.fill(base, day, HourVolume{Symbol: "SBER", Buy: 3000, Sell: 2000, Minutes: 60})

	if s.PrintLots != 100 {
		t.Errorf("PrintLots = %.0f, хотим 100 (объём робота за раз)", s.PrintLots)
	}
	if s.VolumeLots != 2000 {
		t.Errorf("VolumeLots = %.0f, хотим 2000", s.VolumeLots)
	}
	if s.DaySideLots != 40000 {
		t.Errorf("DaySideLots = %.0f, хотим 40000 (шорт сравниваем с продажами)", s.DaySideLots)
	}
	if s.HourLots != 5000 {
		t.Errorf("HourLots = %.0f, хотим 5000 (весь оборот бумаги за час)", s.HourLots)
	}
	// 100 лотов принта на 5000 лотов часового оборота — два процента.
	if s.StrengthPct != 2 {
		t.Errorf("StrengthPct = %.2f, хотим 2", s.StrengthPct)
	}

	// Без базы силу не выдумываем.
	s.fill(base, day, HourVolume{})
	if s.StrengthPct != 0 {
		t.Errorf("StrengthPct = %.2f без часового оборота, хотим 0", s.StrengthPct)
	}
}
