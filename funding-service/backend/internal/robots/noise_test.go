package robots

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/source/moexiss"
)

// Молчание робота меряется по времени ленты его бумаги, а не по стенным часам.
// Инструмент вне быстрого источника идёт с задержкой публичного фида в пятнадцать
// минут: по стенным часам такой робот был бы «замолчавшим» с самого рождения —
// в историю запись попадала, а на страницу «Сейчас» не выходила ни разу.
func TestDelayedFeedKeepsRobotActive(t *testing.T) {
	reg, s, rb := registryFixture(t)

	// Лента бумаги дошла ровно до последнего принта робота, а стенные часы ушли
	// на четверть часа вперёд — столько отстаёт публичный фид.
	head := map[string]time.Time{"SBER": rb.LastSeen}
	reg.observe(nil, rb.LastSeen.Add(15*time.Minute), head)

	if !s.Active {
		t.Error("робот закрыт по стенным часам: на отстающей ленте он не появится на странице никогда")
	}
}

// Но замершая лента — другое дело: у бумаги, переставшей торговаться, голова
// стоит на месте, и без отдельной границы её роботы висели бы на странице до
// конца дня.
func TestFrozenFeedClosesRobot(t *testing.T) {
	reg, s, rb := registryFixture(t)

	head := map[string]time.Time{"SBER": rb.LastSeen}
	reg.observe(nil, rb.LastSeen.Add(feedSilence+time.Minute), head)

	if s.Active {
		t.Error("лента бумаги встала больше чем на feedSilence, а робот всё ещё активен")
	}
}

// Регистр тикера приводится к одному виду: срочный рынок ISS пишет контракты
// вперемешку («SiU6»), быстрый источник — заглавными. Пока регистры расходились,
// один контракт жил двумя лентами, и водяная метка чужую не накрывала.
func TestFuturesTickerCaseIsNormalized(t *testing.T) {
	c := newTestCollector(&fakeISS{}, nil, base.Add(time.Minute))
	futures := MarketTape{Name: "срочный рынок", Engine: futEngine, Market: futMarket}

	// Быстрый источник закрыл ленту контракта до base+10s.
	for i := 0; i < 3; i++ {
		c.ingestStream(Print{
			Symbol: "SIU6", Time: base.Add(time.Duration(i*5) * time.Second),
			Price: 83000, Qty: 5, Side: SideBuy,
		})
	}

	// ISS приносит те же сделки под своим написанием и одну свежее метки.
	c.ingest(futures, []moexiss.Trade{
		{TradeNo: 1, SecID: "SiU6", Price: 83000, Quantity: 5, Side: "B", Timestamp: base},
		{TradeNo: 2, SecID: "SiU6", Price: 83000, Quantity: 5, Side: "B", Timestamp: base.Add(5 * time.Second)},
		{TradeNo: 3, SecID: "SiU6", Price: 83000, Quantity: 5, Side: "B", Timestamp: base.Add(10 * time.Second)},
		{TradeNo: 4, SecID: "SiU6", Price: 83000, Quantity: 5, Side: "B", Timestamp: base.Add(15 * time.Second)},
	})

	if got := c.det.TapeLen("SiU6"); got != 0 {
		t.Errorf("лента «SiU6» держит %d принтов: контракт не должен раздваиваться по регистру", got)
	}
	if got := c.det.TapeLen("SIU6"); got != 4 {
		t.Errorf("в ленте «SIU6» %d принтов, хотим 4: три из потока плюс одна свежая из ISS", got)
	}
}

// Валютный фьючерс узнаётся и в написании ISS: иначе ему достался бы порог
// обнаружения обычной бумаги.
func TestCurrencyMarkedFromMixedCaseTicker(t *testing.T) {
	c := newTestCollector(&fakeISS{}, nil, base.Add(time.Minute))
	futures := MarketTape{Name: "срочный рынок", Engine: futEngine, Market: futMarket}

	c.ingest(futures, []moexiss.Trade{
		{TradeNo: 1, SecID: "SiU6", Price: 83000, Quantity: 5, Side: "B", Timestamp: base},
	})

	if !c.det.currency["SIU6"] {
		t.Error("контракт «SiU6» не помечен валютным")
	}
}

// В режиме TQBR торгуются не только акции: там же сто с лишним паёв БПИФ и
// облигации, у которых маркетмейкер весь день печатает один лот через ровный
// промежуток. Формально робот, по сути — шум, вытесняющий настоящие находки.
func TestStockUniverseKeepsSharesOnly(t *testing.T) {
	w, err := NewWatchlist("")
	if err != nil {
		t.Fatalf("NewWatchlist: %v", err)
	}
	stocks := MarketTape{Name: "акции", Engine: stockEngine, Market: stockMarket, Board: stockBoard}

	// Справочника нет — работаем как раньше, по всему режиму.
	if !w.Keep("AKMP", stocks) {
		t.Error("без справочника наблюдение должно идти по всему режиму")
	}

	w.SetStockUniverse(map[string]bool{"SBER": true, "SNGSP": true})
	if !w.Keep("SBER", stocks) || !w.Keep("sber", stocks) {
		t.Error("акция из справочника выпала из наблюдения")
	}
	if w.Keep("AKMP", stocks) {
		t.Error("пай биржевого фонда остался в наблюдении")
	}
	if w.StockUniverseSize() != 2 {
		t.Errorf("в справочнике %d бумаг, хотим 2", w.StockUniverseSize())
	}

	// Пустой ответ ISS не должен обнулять наблюдение.
	w.SetStockUniverse(nil)
	if !w.Keep("SBER", stocks) || w.StockUniverseSize() != 2 {
		t.Error("пустой справочник затёр прежний")
	}
}

func TestKeepSecurityByType(t *testing.T) {
	cases := map[string]bool{
		"1": true,  // обыкновенная акция
		"2": true,  // привилегированная
		"D": true,  // депозитарная расписка
		"J": false, // пай биржевого фонда
		"B": false, // облигация
		"9": false, // пай закрытого фонда
		"":  false,
	}
	for typ, want := range cases {
		if got := KeepSecurity(typ); got != want {
			t.Errorf("KeepSecurity(%q) = %v, хотим %v", typ, got, want)
		}
	}
}

// Справочник читается с биржи и сужает наблюдение; недоступный справочник сбор
// не ломает.
func TestRefreshUniverse(t *testing.T) {
	client := &fakeISS{securities: []moexiss.Security{
		{SecID: "SBER", SecType: "1"},
		{SecID: "SNGSP", SecType: "2"},
		{SecID: "AKMP", SecType: "J"},
		{SecID: "RU000A0JP773", SecType: "B"},
	}}
	watch, _ := NewWatchlist("")
	opts := DefaultCollectorOptions()
	opts.Watch = watch
	c := NewCollector(client, nil, opts, zerolog.Nop())

	c.refreshUniverse(context.Background())
	if got := watch.StockUniverseSize(); got != 2 {
		t.Fatalf("в справочнике %d бумаг, хотим 2 (SBER и SNGSP)", got)
	}

	// ISS не ответил — прежний справочник остаётся в силе.
	c.client = &fakeISS{}
	c.refreshUniverse(context.Background())
	if got := watch.StockUniverseSize(); got != 2 {
		t.Errorf("после неудачного запроса в справочнике %d бумаг, хотим прежние 2", got)
	}
}
