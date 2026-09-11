package robots

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// feedPrints кладёт сделки прямо в детектор коллектора — так же, как это делает
// быстрый источник.
func feedPrints(c *Collector, prints ...Print) {
	for _, p := range prints {
		c.ingestStream(p)
	}
}

func tapeCollector(now time.Time) *Collector {
	c := NewCollector(&fakeISS{}, nil, DefaultCollectorOptions(), zerolog.Nop())
	c.now = func() time.Time { return now }
	return c
}

// Одна ступенька приказа: три сделки подряд, съеденные одной заявкой, должны
// сложиться в один принт на 372 лота.
func TestTapeMergesOrderIntoOnePrint(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	c := tapeCollector(now)

	feedPrints(c,
		Print{Symbol: "SIU6", Time: now.Add(-10 * time.Second), Price: 81.1, Qty: 50, Side: SideBuy},
		Print{Symbol: "SIU6", Time: now.Add(-10*time.Second + 20*time.Millisecond), Price: 81.2, Qty: 200, Side: SideBuy},
		Print{Symbol: "SIU6", Time: now.Add(-10*time.Second + 40*time.Millisecond), Price: 81.3, Qty: 122, Side: SideBuy},
		Print{Symbol: "SIU6", Time: now.Add(-5 * time.Second), Price: 81.0, Qty: 7, Side: SideSell},
	)

	tape := c.Tape("SIU6", 100, true)
	if len(tape) != 2 {
		t.Fatalf("склеенная лента: %d строк, ждали 2: %+v", len(tape), tape)
	}
	// Свежее впереди — как в терминале.
	if tape[0].Side != SideSell || tape[0].Qty != 7 {
		t.Errorf("первой должна идти последняя сделка, а пришло %+v", tape[0])
	}
	order := tape[1]
	if order.Qty != 372 {
		t.Errorf("объём приказа %v, ждали 372", order.Qty)
	}
	if order.Trades != 3 {
		t.Errorf("сделок в принте %d, ждали 3", order.Trades)
	}
	// Цена приказа — та, до которой он доехал.
	if order.Price != 81.3 {
		t.Errorf("цена приказа %v, ждали 81.3", order.Price)
	}
}

// Сырая лента показывает сделки биржи как есть: по ней проверяют саму склейку.
func TestTapeRawKeepsExchangeTrades(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	c := tapeCollector(now)

	feedPrints(c,
		Print{Symbol: "SIU6", Time: now.Add(-time.Second), Price: 81.1, Qty: 50, Side: SideBuy},
		Print{Symbol: "SIU6", Time: now.Add(-time.Second + 20*time.Millisecond), Price: 81.2, Qty: 200, Side: SideBuy},
	)

	tape := c.Tape("SIU6", 100, false)
	if len(tape) != 2 {
		t.Fatalf("сырая лента: %d строк, ждали 2", len(tape))
	}
	for _, p := range tape {
		if p.Trades != 1 {
			t.Errorf("у сырой сделки должна стоять одна сделка в принте, а стоит %d", p.Trades)
		}
	}
}

// Ограничение отрезает СТАРОЕ: на экране нужны последние принты, а не первые.
func TestTapeLimitKeepsFreshPrints(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	c := tapeCollector(now)

	for i := range 10 {
		c.ingestStream(Print{
			Symbol: "SIU6",
			Time:   now.Add(time.Duration(i-10) * time.Second),
			Price:  81 + float64(i),
			Qty:    1,
			Side:   SideBuy,
		})
	}

	tape := c.Tape("SIU6", 3, true)
	if len(tape) != 3 {
		t.Fatalf("ждали 3 строки, пришло %d", len(tape))
	}
	if tape[0].Price != 90 {
		t.Errorf("первой должна идти самая свежая цена 90, а пришла %v", tape[0].Price)
	}
	if tape[2].Price != 88 {
		t.Errorf("последней в тройке ждали 88, а пришла %v", tape[2].Price)
	}
}

// Неизвестный тикер — пустая лента, а не паника и не nil в JSON.
func TestTapeUnknownSymbol(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	c := tapeCollector(now)
	if tape := c.Tape("NOPE", 10, true); len(tape) != 0 || tape == nil {
		t.Fatalf("ждали пустую непустую-nil ленту, пришло %+v", tape)
	}
}
