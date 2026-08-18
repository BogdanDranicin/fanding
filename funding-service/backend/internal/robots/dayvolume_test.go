package robots

import (
	"testing"
	"time"
)

func dayPrint(no int64, sym string, ts time.Time, qty float64, side Side) Print {
	return Print{TradeNo: no, Symbol: sym, Time: ts, Price: 100, Qty: qty, Side: side}
}

// Оборот копится раздельно по сторонам агрессора.
func TestDayVolumeSplitsBySide(t *testing.T) {
	v := newDayVolumes()
	v.add(dayPrint(1, "SBER", base, 100, SideBuy))
	v.add(dayPrint(2, "SBER", base.Add(time.Second), 40, SideSell))
	v.add(dayPrint(3, "SBER", base.Add(2*time.Second), 60, SideBuy))

	got := v.get("SBER", base)
	if got.Buy != 160 {
		t.Errorf("Buy = %.0f, хотим 160", got.Buy)
	}
	if got.Sell != 40 {
		t.Errorf("Sell = %.0f, хотим 40", got.Sell)
	}
	if got.Total() != 200 {
		t.Errorf("Total = %.0f, хотим 200", got.Total())
	}
	if got.Trades != 3 {
		t.Errorf("Trades = %d, хотим 3", got.Trades)
	}
	if got.Side(SideSell) != 40 || got.Side(SideBuy) != 160 {
		t.Errorf("Side отдаёт не ту сторону: %+v", got)
	}
}

// Пересев курсора заставляет коллектор перечитать хвост ленты; те же сделки
// не должны лечь в оборот дважды.
func TestDayVolumeIgnoresRepeatedTrades(t *testing.T) {
	v := newDayVolumes()
	for i := int64(1); i <= 3; i++ {
		v.add(dayPrint(i, "SBER", base.Add(time.Duration(i)*time.Second), 100, SideBuy))
	}
	// Хвост пришёл повторно, плюс одна новая сделка.
	for i := int64(1); i <= 4; i++ {
		v.add(dayPrint(i, "SBER", base.Add(time.Duration(i)*time.Second), 100, SideBuy))
	}

	got := v.get("SBER", base)
	if got.Buy != 400 {
		t.Errorf("Buy = %.0f, хотим 400 (4 сделки по 100, без повторного счёта)", got.Buy)
	}
	if got.Trades != 4 {
		t.Errorf("Trades = %d, хотим 4", got.Trades)
	}
}

// Смена биржевого дня обнуляет счётчики: сила робота не считается от вчерашней базы.
func TestDayVolumeResetsOnNewDay(t *testing.T) {
	v := newDayVolumes()
	v.add(dayPrint(500, "SBER", base, 1000, SideBuy))

	next := base.Add(24 * time.Hour)
	v.add(dayPrint(1, "SBER", next, 7, SideBuy))

	got := v.get("SBER", next)
	if got.Buy != 7 {
		t.Errorf("Buy = %.0f, хотим 7: вчерашний оборот в новый день не переносится", got.Buy)
	}
	if got.Date != next.In(msk).Format("2006-01-02") {
		t.Errorf("Date = %q, хотим день сделки", got.Date)
	}

	// Запрос за вчера уже не обслуживается — база дня другая.
	if old := v.get("SBER", base); old.Total() != 0 {
		t.Errorf("за прошлый день отдано %.0f лотов, хотим 0", old.Total())
	}
}

// Пустой инструмент отдаёт нули, а не панику.
func TestDayVolumeUnknownSymbol(t *testing.T) {
	v := newDayVolumes()
	if got := v.get("GAZP", base); got.Total() != 0 || got.Symbol != "GAZP" {
		t.Errorf("для неизвестного тикера хотим пустой оборот, получили %+v", got)
	}
	if got := v.snapshot(); len(got) != 0 {
		t.Errorf("снимок непустой: %+v", got)
	}
}
