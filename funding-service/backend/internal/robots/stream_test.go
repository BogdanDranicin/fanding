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
	symbols []StreamInstrument
	symErr  error
	prints  []Print
	runErr  error
	runs    int
}

func (f *fakeStream) Symbols(context.Context) ([]StreamInstrument, error) {
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

// Из каталога быстрого источника берём только то, за чем следим.
//
// Каталог брокера, в отличие от ленты ISS, ничем не сужен: в нём и иностранные
// бумаги, и весь товарный срочный рынок. Без сопоставления с режимом торгов сюда
// проходило всё подряд — подписка раздувалась с двух сотен инструментов до двух
// с половиной тысяч.
func TestStreamSymbolsIntersectWatchlist(t *testing.T) {
	opts := DefaultCollectorOptions()
	opts.Stream = &fakeStream{symbols: []StreamInstrument{
		{Symbol: "SBER", Board: stockBoard},
		{Symbol: "AKMP", Board: stockBoard},
		{Symbol: "MXU6", Board: fortsBoard},
		{Symbol: "USDRUBF", Board: fortsBoard},
		{Symbol: "SVU6", Board: fortsBoard},   // серебро — не берём
		{Symbol: "CCQ6", Board: fortsBoard},   // какао — не берём
		{Symbol: "AAPL", Board: "SPBXM"},      // иностранная бумага не с нашей ленты
		{Symbol: "SU26238RMFS4", Board: "TQOB"}, // облигация
	}}
	c := NewCollector(&fakeISS{}, nil, opts, zerolog.Nop())

	got, err := c.streamSymbols(context.Background())
	if err != nil {
		t.Fatalf("streamSymbols: %v", err)
	}
	want := []string{"SBER", "AKMP", "MXU6", "USDRUBF"}
	if len(got) != len(want) {
		t.Fatalf("отобрано %v, хотим %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("отобрано %v, хотим %v", got, want)
			break
		}
	}
}

// Обрыв потока не должен ронять сбор: коллектор ждёт и подключается снова.
func TestStreamLoopRetriesAfterBreak(t *testing.T) {
	stream := &fakeStream{
		symbols: []StreamInstrument{{Symbol: "SBER", Board: stockBoard}},
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

// Отставание потока меряется, а не берётся из головы: страница пишет пользователю
// реальную свежесть ленты, и она у потока брокера на три порядка меньше, чем у ISS.
func TestStreamStatusMeasuresLag(t *testing.T) {
	now := base.Add(2 * time.Second)
	c := newTestCollector(&fakeISS{}, nil, now)

	if got := c.StreamStatus(); got.Enabled {
		t.Errorf("без настроенного источника Enabled = true: %+v", got)
	}

	for i := 0; i < 20; i++ {
		c.ingestStream(Print{Symbol: "SBER", Time: base, Price: 250, Qty: 100, Side: SideBuy})
	}
	st := c.StreamStatus()
	if st.LagMs != 2000 {
		t.Errorf("отставание %d мс, хотим 2000: все принты пришли с одинаковым запозданием", st.LagMs)
	}
	if !st.LastPrintAt.Equal(base) {
		t.Errorf("LastPrintAt = %v, хотим %v", st.LastPrintAt, base)
	}
}

// Хвост, досланный после переподключения, — это не измерение скорости: полчаса
// отставания одного принта не должны превратить живой поток в «отстаёт на полчаса».
func TestStreamStatusIgnoresReplayedBacklog(t *testing.T) {
	now := base.Add(time.Second)
	c := newTestCollector(&fakeISS{}, nil, now)

	c.ingestStream(Print{Symbol: "SBER", Time: base, Price: 250, Qty: 100, Side: SideBuy})
	c.ingestStream(Print{
		Symbol: "SBER", Time: now.Add(-30 * time.Minute),
		Price: 250, Qty: 100, Side: SideBuy,
	})

	if got := c.StreamStatus().LagMs; got != 1000 {
		t.Errorf("отставание %d мс, хотим 1000: старый хвост в среднее не идёт", got)
	}
}

// Обрыв и переподключение видны странице: пока соединения нет, лента идёт из ISS
// и «время до удара» перестаёт быть почти-наблюдением.
func TestStreamStatusFollowsConnection(t *testing.T) {
	stream := &fakeStream{
		symbols: []StreamInstrument{{Symbol: "SBER", Board: testTape.Board}},
		runErr:  errors.New("обрыв"),
	}
	c := newTestCollector(&fakeISS{}, nil, base)
	c.opts.Stream = stream
	c.opts.StreamRetry = func(int) time.Duration { return time.Hour }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.streamLoop(ctx)
	}()

	// Ждём, пока цикл дойдёт до паузы после обрыва: соединения нет, но список
	// инструментов уже получен.
	deadline := time.Now().Add(2 * time.Second)
	for {
		st := c.StreamStatus()
		if st.Symbols == 1 && !st.Connected && stream.runs > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("не дождались состояния после обрыва: %+v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	<-done
	if got := c.StreamStatus(); got.Connected {
		t.Errorf("после отмены Connected = true: %+v", got)
	}
}
