package main

import (
	"testing"
	"time"

	"github.com/funding-service/backend/internal/source/moexiss"
)

// tradeAt собирает сделку ленты на указанное время МСК того же дня.
func tradeAt(day string, clock string, price, qty float64) moexiss.Trade {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", day+" "+clock, mskZone)
	if err != nil {
		panic(err)
	}
	return moexiss.Trade{Price: price, Quantity: qty, Timestamp: t}
}

const backstopDay = "2026-09-09"

// Нога считается по сделкам окна 10:00–15:30 и только по ним: утренняя сессия
// (с 07:00 по ЕТС) и вечерняя в средневзвешенную не входят.
func TestWindowVWAPCountsOnlyTheWindow(t *testing.T) {
	trades := []moexiss.Trade{
		tradeAt(backstopDay, "07:02:00", 90, 100), // утро ЕТС — мимо
		tradeAt(backstopDay, "10:00:02", 100, 1),
		tradeAt(backstopDay, "12:00:00", 102, 3),
		tradeAt(backstopDay, "15:29:52", 98, 4),
		tradeAt(backstopDay, "15:30:00", 200, 500), // за границей — только доказательство
		tradeAt(backstopDay, "18:40:00", 300, 900), // вечёрка — мимо
	}

	leg, ok := windowVWAP(trades, backstopDay)
	if !ok {
		t.Fatalf("нога не собралась: %+v", leg)
	}
	want := (100*1 + 102*3 + 98*4) / 8.0
	if diff := leg.vwap - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("VWAP окна = %.6f, хотим %.6f", leg.vwap, want)
	}
	if leg.volume != 8 {
		t.Errorf("объём окна = %v, хотим 8", leg.volume)
	}
	if leg.prints != 3 {
		t.Errorf("сделок в окне = %d, хотим 3", leg.prints)
	}
	if leg.post != 2 {
		t.Errorf("сделок за 15:30 = %d, хотим 2", leg.post)
	}
}

// Лента, оборвавшаяся внутри окна, ногой не считается: отличить полное окно от
// обрезанного нечем, а ровно на этом и горели — обрезанное окно уходило в бот.
func TestWindowVWAPRejectsTapeThatNeverCrossedSettl(t *testing.T) {
	trades := []moexiss.Trade{
		tradeAt(backstopDay, "10:00:02", 100, 1),
		tradeAt(backstopDay, "15:11:00", 99, 5),
	}
	if leg, ok := windowVWAP(trades, backstopDay); ok {
		t.Errorf("обрезанная лента принята как нога: %+v", leg)
	}
}

// Адресные сделки идут мимо стакана, и биржевой VWAP их не учитывает.
func TestWindowVWAPSkipsOffMarket(t *testing.T) {
	off := tradeAt(backstopDay, "11:00:00", 500, 1000)
	off.OffMarket = true
	trades := []moexiss.Trade{
		tradeAt(backstopDay, "10:30:00", 100, 2),
		off,
		tradeAt(backstopDay, "15:31:00", 100, 1),
	}

	leg, ok := windowVWAP(trades, backstopDay)
	if !ok {
		t.Fatalf("нога не собралась: %+v", leg)
	}
	if leg.vwap != 100 {
		t.Errorf("VWAP = %v, хотим 100: адресная сделка не должна попадать в окно", leg.vwap)
	}
}

// Сделку чужого календарного дня биржа приписывает к сегодняшней сессии, но её
// цена относится к другому дню и в средневзвешенную не входит.
func TestWindowVWAPSkipsBackdated(t *testing.T) {
	back := tradeAt(backstopDay, "10:15:00", 500, 1000)
	back.Backdated = true
	trades := []moexiss.Trade{
		tradeAt(backstopDay, "10:30:00", 100, 2),
		back,
		tradeAt(backstopDay, "15:31:00", 100, 1),
	}

	leg, ok := windowVWAP(trades, backstopDay)
	if !ok {
		t.Fatalf("нога не собралась: %+v", leg)
	}
	if leg.vwap != 100 {
		t.Errorf("VWAP = %v, хотим 100: сделка чужого дня не должна попадать в окно", leg.vwap)
	}
}

// Лента за чужой день ногой сегодняшнего дня быть не может.
func TestWindowVWAPSkipsOtherDay(t *testing.T) {
	trades := []moexiss.Trade{
		tradeAt("2026-09-08", "12:00:00", 100, 5),
		tradeAt("2026-09-08", "16:00:00", 100, 5),
	}
	if leg, ok := windowVWAP(trades, backstopDay); ok {
		t.Errorf("лента за 08.09 принята как нога за 09.09: %+v", leg)
	}
}

// Реконструкция боевого случая: настоящая нога EURRUBF за 09.09.2026 — 99.50425,
// а движок в тот день заморозился на приближении 99.41258, и подписчикам ушёл
// фандинг +0.06208 против биржевого SWAPRATE 0.15075.
func TestWindowVWAPReproducesRealLeg(t *testing.T) {
	// Три сделки с теми же весами, что дают биржевую среднюю по окну.
	trades := []moexiss.Trade{
		tradeAt(backstopDay, "10:00:02", 100.07745, 94),
		tradeAt(backstopDay, "13:30:00", 99.50000, 14933),
		tradeAt(backstopDay, "15:29:52", 99.48000, 4115),
		tradeAt(backstopDay, "15:30:01", 99.40000, 10),
	}

	leg, ok := windowVWAP(trades, backstopDay)
	if !ok {
		t.Fatalf("нога не собралась: %+v", leg)
	}
	// Проверяем не конкретное число, а то, что нога стоит выше приближения,
	// на котором сервис ошибся: приближение занижало её почти на десять копеек.
	const wrong = 99.41258
	if leg.vwap <= wrong {
		t.Errorf("нога = %.5f, а приближение давало %.5f — реконструкция обязана быть выше", leg.vwap, wrong)
	}
}
