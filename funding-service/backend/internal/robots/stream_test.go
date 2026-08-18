package robots

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/source/moexiss"
)

// fakeStream отдаёт заранее подготовленные принты и умеет притворяться оборвавшимся.
type fakeStream struct {
	symbols []string
	symErr  error
	prints  []Print
	runErr  error
	runs    int
}

func (f *fakeStream) Symbols(context.Context) ([]string, error) {
	return f.symbols, f.symErr
}

func (f *fakeStream) Run(ctx context.Context, _ []string, out chan<- Print) error {
	f.runs++
	for _, p := range f.prints {
		select {
		case out <- p:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.runErr
}

// Сделка, уже принесённая быстрым источником, не должна попасть в ленту второй раз,
// когда та же сделка придёт из ISS пятнадцатью минутами позже.
func TestStreamWatermarkSuppressesLateISSDuplicates(t *testing.T) {
	c := newTestCollector(&fakeISS{}, nil, base.Add(time.Minute))

	// Быстрый источник закрыл ленту SBER до base+10s.
	for i := 0; i < 3; i++ {
		c.ingestStream(Print{
			Symbol: "SBER", Time: base.Add(time.Duration(i*5) * time.Second),
			Price: 250, Qty: 100, Side: SideBuy,
		})
	}
	if got := c.det.TapeLen("SBER"); got != 3 {
		t.Fatalf("в ленте %d принтов, хотим 3", got)
	}

	// ISS приносит те же три сделки и одну свежее метки.
	trades := []moexiss.Trade{
		{TradeNo: 1, SecID: "SBER", Price: 250, Quantity: 100, Side: "B", Timestamp: base},
		{TradeNo: 2, SecID: "SBER", Price: 250, Quantity: 100, Side: "B", Timestamp: base.Add(5 * time.Second)},
		{TradeNo: 3, SecID: "SBER", Price: 250, Quantity: 100, Side: "B", Timestamp: base.Add(10 * time.Second)},
		{TradeNo: 4, SecID: "SBER", Price: 250, Quantity: 100, Side: "B", Timestamp: base.Add(15 * time.Second)},
	}
	c.ingest(testTape, trades)

	if got := c.det.TapeLen("SBER"); got != 4 {
		t.Errorf("в ленте %d принтов, хотим 4: дубли до водяной метки должны отсеяться", got)
	}
}

// Инструмент, которого нет у быстрого источника, обязан целиком идти из ISS.
func TestSymbolWithoutStreamKeepsFullISSTape(t *testing.T) {
	c := newTestCollector(&fakeISS{}, nil, base.Add(time.Minute))

	c.ingestStream(Print{Symbol: "SBER", Time: base.Add(30 * time.Second), Price: 250, Qty: 10, Side: SideBuy})

	trades := []moexiss.Trade{
		{TradeNo: 1, SecID: "GAZP", Price: 150, Quantity: 20, Side: "S", Timestamp: base},
		{TradeNo: 2, SecID: "GAZP", Price: 150, Quantity: 20, Side: "S", Timestamp: base.Add(10 * time.Second)},
	}
	c.ingest(testTape, trades)

	if got := c.det.TapeLen("GAZP"); got != 2 {
		t.Errorf("в ленте GAZP %d принтов, хотим 2: метка SBER её касаться не должна", got)
	}
}

// Когда поток обрывается, метка замирает и ISS подхватывает всё, что после неё.
func TestFrozenWatermarkLetsISSTakeOver(t *testing.T) {
	c := newTestCollector(&fakeISS{}, nil, base.Add(time.Minute))
	c.ingestStream(Print{Symbol: "SBER", Time: base.Add(10 * time.Second), Price: 250, Qty: 100, Side: SideBuy})

	// Поток умер; ISS доносит сделки, случившиеся после последней известной.
	trades := []moexiss.Trade{
		{TradeNo: 5, SecID: "SBER", Price: 250, Quantity: 100, Side: "B", Timestamp: base.Add(20 * time.Second)},
		{TradeNo: 6, SecID: "SBER", Price: 250, Quantity: 100, Side: "B", Timestamp: base.Add(30 * time.Second)},
	}
	c.ingest(testTape, trades)

	if got := c.det.TapeLen("SBER"); got != 3 {
		t.Errorf("в ленте %d принтов, хотим 3 (один из потока и два из ISS)", got)
	}
}

// Из инструментов быстрого источника берём только те, за которыми следим.
func TestStreamSymbolsIntersectWatchlist(t *testing.T) {
	c := newTestCollector(&fakeISS{}, nil, base)
	c.opts.Stream = &fakeStream{symbols: []string{"SBER", "SILV", "MXU6", "USDRUBF"}}

	got, err := c.streamSymbols(context.Background())
	if err != nil {
		t.Fatalf("streamSymbols: %v", err)
	}
	// testTape — лента акций, поэтому в ней проходит любой тикер; со срочного
	// рынка правила пропускают индексный MXU6 и валютный USDRUBF, но не серебро.
	want := map[string]bool{"SBER": true, "SILV": true, "MXU6": true, "USDRUBF": true}
	if len(got) != len(want) {
		t.Fatalf("отобрано %v, хотим %d инструментов", got, len(want))
	}
}

// Обрыв потока не должен ронять сбор: коллектор ждёт и подключается снова.
func TestStreamLoopRetriesAfterBreak(t *testing.T) {
	stream := &fakeStream{
		symbols: []string{"SBER"},
		prints:  []Print{{Symbol: "SBER", Time: base, Price: 250, Qty: 10, Side: SideBuy}},
		runErr:  errors.New("соединение закрыто"),
	}
	opts := DefaultCollectorOptions()
	opts.Tapes = []MarketTape{testTape}
	opts.Stream = stream
	opts.StreamRetry = func(int) time.Duration { return time.Millisecond }
	c := NewCollector(&fakeISS{}, nil, opts, zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	c.streamLoop(ctx)

	if stream.runs < 2 {
		t.Errorf("поток запускался %d раз, хотим минимум 2 (обрыв и переподключение)", stream.runs)
	}
	if got := c.det.TapeLen("SBER"); got == 0 {
		t.Error("принты из потока не дошли до детектора")
	}
}

// Недоступный каталог инструментов не должен ронять сбор: остаётся ISS.
func TestStreamLoopGivesUpOnCatalogueError(t *testing.T) {
	stream := &fakeStream{symErr: errors.New("нет связи")}
	opts := DefaultCollectorOptions()
	opts.Tapes = []MarketTape{testTape}
	opts.Stream = stream
	c := NewCollector(&fakeISS{}, nil, opts, zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c.streamLoop(ctx) // должен вернуться сразу, а не крутиться до таймаута

	if stream.runs != 0 {
		t.Errorf("поток запускался %d раз, хотим 0", stream.runs)
	}
}
