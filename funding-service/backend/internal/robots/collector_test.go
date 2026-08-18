package robots

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/source/moexiss"
)

// fakeISS отдаёт заранее собранную ленту вместо похода в сеть.
type fakeISS struct {
	tail  []moexiss.Trade
	calls int
}

func (f *fakeISS) FetchTradeTail(context.Context, moexiss.TradeFeed, int) ([]moexiss.Trade, error) {
	f.calls++
	return f.tail, nil
}

func (f *fakeISS) FetchTradesOn(context.Context, moexiss.TradeFeed, int64) ([]moexiss.Trade, error) {
	return nil, nil
}

// fakeStore считает вставки и обновления и раздаёт идентификаторы.
type fakeStore struct {
	inserted []RobotRow
	updated  []RobotRow
	nextID   int64
}

func (s *fakeStore) UpsertRobot(_ context.Context, in RobotRow) (int64, error) {
	if in.ID == 0 {
		s.nextID++
		in.ID = s.nextID
		s.inserted = append(s.inserted, in)
		return in.ID, nil
	}
	s.updated = append(s.updated, in)
	return in.ID, nil
}

// testTape — лента основного режима акций, из которой берут сделки тесты.
var testTape = MarketTape{Name: "тест", Engine: stockEngine, Market: stockMarket, Board: stockBoard}

// tapeWithRobot собирает ленту ISS: шум плюс серия робота заданного размера и периода.
func tapeWithRobot(start time.Time, periodSec float64, n int, qty float64, side string) []moexiss.Trade {
	var out []moexiss.Trade
	var no int64
	for i := 0; i < n; i++ {
		no++
		out = append(out, moexiss.Trade{
			TradeNo:   no,
			SecID:     "SBER",
			Price:     250,
			Quantity:  qty,
			Timestamp: start.Add(time.Duration(float64(i) * periodSec * float64(time.Second))).Truncate(time.Second),
			Side:      side,
		})
	}
	// Адресная сделка того же размера: она идёт мимо стакана и в анализ попасть не должна.
	no++
	out = append(out, moexiss.Trade{
		TradeNo: no, SecID: "SBER", Price: 250, Quantity: qty, Side: side,
		Timestamp: start.Add(3 * time.Second), OffMarket: true,
	})
	// Сделка чужого календарного дня, доложенная биржей в текущую сессию.
	no++
	out = append(out, moexiss.Trade{
		TradeNo: no, SecID: "SBER", Price: 250, Quantity: qty, Side: side,
		Timestamp: start.Add(7 * time.Second), Backdated: true,
	})
	return out
}

func newTestCollector(client issClient, store Store, now time.Time) *Collector {
	opts := DefaultCollectorOptions()
	opts.Tapes = []MarketTape{testTape}
	c := NewCollector(client, store, opts, zerolog.Nop())
	c.now = func() time.Time { return now }
	return c
}

// Сквозной путь: сделки ISS → принты → детектор → сессия → строка в базе.
func TestCollectorFindsRobotAndPersists(t *testing.T) {
	start := base
	tape := tapeWithRobot(start, 11.2, 60, 3012, "S")
	client := &fakeISS{tail: tape}
	store := &fakeStore{}

	now := start.Add(12 * time.Minute)
	c := newTestCollector(client, store, now)

	trades, err := client.FetchTradeTail(context.Background(), moexiss.TradeFeed{}, seedPages)
	if err != nil {
		t.Fatalf("FetchTradeTail: %v", err)
	}
	c.ingest(testTape, trades)
	c.scanOnce(context.Background())

	snap := c.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("в срезе %d роботов, хотим 1: %+v", len(snap), snap)
	}
	got := snap[0]
	if got.Symbol != "SBER" {
		t.Errorf("Symbol = %q, хотим SBER", got.Symbol)
	}
	if got.Side != SideSell {
		t.Errorf("Side = %q, хотим %q (BUYSELL=S)", got.Side, SideSell)
	}
	if math.Abs(got.PeriodSec-11.2) > 0.15 {
		t.Errorf("PeriodSec = %.3f, хотим 11.2", got.PeriodSec)
	}
	if math.Round(got.QtyTypical) != 3012 {
		t.Errorf("QtyTypical = %.0f, хотим 3012", got.QtyTypical)
	}
	if !got.Active {
		t.Error("робот должен считаться работающим")
	}
	if got.ID == 0 {
		t.Error("сессии не присвоен идентификатор строки базы")
	}

	if len(store.inserted) != 1 {
		t.Fatalf("в базу вставлено %d строк, хотим 1", len(store.inserted))
	}
	if store.inserted[0].Side != "S" {
		t.Errorf("в базе side = %q, хотим S", store.inserted[0].Side)
	}
}

// Повторный скан той же серии обновляет строку, а не заводит вторую.
func TestCollectorUpdatesSessionInsteadOfDuplicating(t *testing.T) {
	start := base
	client := &fakeISS{tail: tapeWithRobot(start, 9, 80, 500, "B")}
	store := &fakeStore{}
	c := newTestCollector(client, store, start.Add(11*time.Minute))

	trades, _ := client.FetchTradeTail(context.Background(), moexiss.TradeFeed{}, seedPages)
	c.ingest(testTape, trades)
	c.scanOnce(context.Background())
	c.scanOnce(context.Background())

	if len(store.inserted) != 1 {
		t.Errorf("вставок %d, хотим 1: серия та же", len(store.inserted))
	}
	if len(store.updated) == 0 {
		t.Error("второй скан не обновил строку")
	}
	if n := len(c.Snapshot()); n != 1 {
		t.Errorf("в срезе %d сессий, хотим 1", n)
	}
}

// Замолчавший робот закрывается, но из истории не исчезает.
func TestCollectorClosesStaleRobot(t *testing.T) {
	start := base
	client := &fakeISS{tail: tapeWithRobot(start, 9, 80, 500, "B")}
	store := &fakeStore{}
	c := newTestCollector(client, store, start.Add(11*time.Minute))

	trades, _ := client.FetchTradeTail(context.Background(), moexiss.TradeFeed{}, seedPages)
	c.ingest(testTape, trades)
	c.scanOnce(context.Background())
	if !c.Snapshot()[0].Active {
		t.Fatal("робот должен быть активен сразу после серии")
	}

	// Время ушло далеко вперёд: новых принтов нет, серия должна закрыться.
	c.now = func() time.Time { return start.Add(2 * time.Hour) }
	c.scanOnce(context.Background())

	snap := c.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("в срезе %d сессий, хотим 1 (закрытую)", len(snap))
	}
	if snap[0].Active {
		t.Error("робот молчит два часа, но всё ещё помечен работающим")
	}
}

// Список наблюдения: пустая настройка означает правила по умолчанию, непустая —
// только перечисленные тикеры.
func TestWatchlist(t *testing.T) {
	shares := MarketTape{Name: "акции", Engine: stockEngine, Market: stockMarket, Board: stockBoard}
	forts := MarketTape{Name: "срочный", Engine: futEngine, Market: futMarket}

	byRules, err := NewWatchlist("")
	if err != nil {
		t.Fatalf("NewWatchlist(\"\"): %v", err)
	}
	cases := []struct {
		secid string
		tape  MarketTape
		want  bool
		why   string
	}{
		{"SBER", shares, true, "акции основного режима берём все"},
		{"AKMP", shares, true, "в том числе неликвид"},
		{"SBERF", forts, true, "вечный контракт на акцию"},
		{"GAZPF", forts, true, "вечный контракт на акцию"},
		{"USDRUBF", forts, true, "валютный вечный"},
		{"Si-9.26", forts, true, "валютный квартальный"},
		{"MIX-9.26", forts, true, "индексный фьючерс"},
		{"MXI-9.26", forts, true, "индексный фьючерс"},
		{"IMOEXF", forts, true, "индексный вечный"},
		{"SILV", forts, false, "товарный контракт не берём"},
		{"CCQ6", forts, false, "прочий квартальный не берём"},
		{"", shares, false, "пустой тикер"},
	}
	for _, tc := range cases {
		if got := byRules.Keep(tc.secid, tc.tape); got != tc.want {
			t.Errorf("Keep(%q) = %v, хотим %v: %s", tc.secid, got, tc.want, tc.why)
		}
	}

	only, err := NewWatchlist(" sber , GAZP,futures:USDRUBF ")
	if err != nil {
		t.Fatalf("NewWatchlist: %v", err)
	}
	if !only.Keep("SBER", shares) || !only.Keep("USDRUBF", forts) {
		t.Error("перечисленные тикеры должны проходить")
	}
	if only.Keep("LKOH", shares) {
		t.Error("тикер вне списка проходить не должен")
	}
	if _, err := NewWatchlist("options:SI"); err == nil {
		t.Error("неизвестный префикс должен быть ошибкой")
	}
}
