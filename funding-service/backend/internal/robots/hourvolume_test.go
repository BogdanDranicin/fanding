package robots

import (
	"testing"
	"time"
)

// В окно попадает только последний час перед якорем: всё, что старше, силу робота
// уже не характеризует.
func TestHourVolumeKeepsLastHour(t *testing.T) {
	v := newHourVolumes()
	v.add(dayPrint(1, "SBER", base, 500, SideBuy))                     // 11:00 — выпадет
	v.add(dayPrint(2, "SBER", base.Add(30*time.Minute), 100, SideBuy)) // 11:30
	v.add(dayPrint(3, "SBER", base.Add(45*time.Minute), 40, SideSell)) // 11:45
	v.add(dayPrint(4, "SBER", base.Add(59*time.Minute), 60, SideBuy))  // 11:59

	got := v.get("SBER", base.Add(time.Hour)) // якорь 12:00, окно 11:01–12:00
	if got.Buy != 160 {
		t.Errorf("Buy = %.0f, хотим 160 (сделка 11:00 вне окна)", got.Buy)
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
	if got.From != "11:01" || got.To != "12:00" {
		t.Errorf("границы окна = %s–%s, хотим 11:01–12:00", got.From, got.To)
	}
	// Минут накрыто ровно столько, в скольких были сделки: по ним видно, что база
	// неполная, и сила по ней завышена.
	if got.Minutes != 3 {
		t.Errorf("Minutes = %d, хотим 3", got.Minutes)
	}
}

// Якорь — последний принт робота, а не стенные часы: лента ISS отстаёт на
// пятнадцать минут, и окно от «сейчас» у такой бумаги было бы обрезано.
func TestHourVolumeAnchorsOnLaggingTape(t *testing.T) {
	v := newHourVolumes()
	for i := 0; i < 60; i++ {
		v.add(dayPrint(int64(i+1), "GAZP", base.Add(time.Duration(i)*time.Minute), 10, SideBuy))
	}

	// Робот последний раз печатал в 11:59, а стенные часы уже 12:15.
	got := v.get("GAZP", base.Add(59*time.Minute))
	if got.Minutes != 60 {
		t.Errorf("Minutes = %d, хотим 60: окно от последнего принта полное", got.Minutes)
	}
	if got.Total() != 600 {
		t.Errorf("Total = %.0f, хотим 600", got.Total())
	}
}

// Кольцо корзин переживает смену часа: старые минуты затираются новыми, а не
// складываются с ними.
func TestHourVolumeRingOverwritesOldMinutes(t *testing.T) {
	v := newHourVolumes()
	v.add(dayPrint(1, "SBER", base, 100, SideBuy))
	// Та же минута суток через два часа — ложится в ту же ячейку кольца.
	v.add(dayPrint(2, "SBER", base.Add(2*time.Hour), 30, SideBuy))

	got := v.get("SBER", base.Add(2*time.Hour))
	if got.Total() != 30 {
		t.Errorf("Total = %.0f, хотим 30: старая минута обязана быть затёрта", got.Total())
	}
}

// Пересев курсора заставляет коллектор перечитать хвост ленты; те же сделки
// не должны лечь в часовой оборот дважды.
func TestHourVolumeIgnoresRepeatedTrades(t *testing.T) {
	v := newHourVolumes()
	v.add(dayPrint(10, "SBER", base, 100, SideBuy))
	v.add(dayPrint(11, "SBER", base.Add(time.Minute), 50, SideBuy))
	v.add(dayPrint(10, "SBER", base, 100, SideBuy)) // повтор
	v.add(dayPrint(11, "SBER", base.Add(time.Minute), 50, SideBuy))

	if got := v.get("SBER", base.Add(time.Minute)); got.Total() != 150 {
		t.Errorf("Total = %.0f, хотим 150", got.Total())
	}
}

// Сбор начался внутри окна — час набран не целиком, и страница обязана это знать.
func TestHourVolumeReportsPartialBase(t *testing.T) {
	v := newHourVolumes()
	v.add(dayPrint(1, "SBER", base.Add(40*time.Minute), 100, SideBuy)) // первое, что видели

	got := v.get("SBER", base.Add(time.Hour)) // окно 11:01–12:00
	if got.Since != "11:40:00" {
		t.Errorf("Since = %q, хотим 11:40:00: сбор начался внутри окна", got.Since)
	}

	// Тот же оборот, но час назад сервис уже стоял на ленте — база полная.
	v.add(dayPrint(2, "SBER", base, 10, SideBuy))
	if got := v.get("SBER", base.Add(time.Hour)); got.Since != "" {
		t.Errorf("Since = %q, хотим пусто: окно накрыто целиком", got.Since)
	}
}

// По инструменту, о котором ещё ничего не знаем, силу считать не от чего.
func TestHourVolumeUnknownSymbol(t *testing.T) {
	v := newHourVolumes()
	got := v.get("LKOH", base)
	if got.Total() != 0 || got.Minutes != 0 || got.From != "" || got.Since != "" {
		t.Errorf("по незнакомой бумаге хотим пустой оборот, получили %+v", got)
	}
}
