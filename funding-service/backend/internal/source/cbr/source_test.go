package cbr_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/text/encoding/charmap"

	"github.com/funding-service/backend/internal/source"
	"github.com/funding-service/backend/internal/source/cbr"
)

// fastInterval returns a constant 20 ms — used to drive tests without real time.
func fastInterval() time.Duration { return 20 * time.Millisecond }

const xmlTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<ValCurs Date=%q name="Foreign Currency Market">
<Valute ID="R01235"><CharCode>USD</CharCode><Nominal>1</Nominal><Value>82,5000</Value></Valute>
<Valute ID="R01239"><CharCode>EUR</CharCode><Nominal>1</Nominal><Value>90,3000</Value></Valute>
</ValCurs>`

func validXML(date string) string { return fmt.Sprintf(xmlTemplate, date) }

func newTestSource(srv *httptest.Server) *cbr.Source {
	return cbr.NewWithURL(srv.URL, fastInterval, zerolog.Nop())
}

// --- interval logic ---

func TestAdaptiveInterval_FastWindow(t *testing.T) {
	msk := time.FixedZone("MSK", 3*60*60)
	cases := []struct {
		h, m int
		fast bool
	}{
		{16, 0, true},
		{16, 30, true},
		{16, 45, true},
		{17, 0, true},
		{17, 59, true},
		{18, 0, true},
		{18, 30, true},
		{18, 59, true},
		{15, 59, false},
		{19, 0, false},
		{11, 30, false},
		{15, 30, false},
	}
	for _, tc := range cases {
		t := t
		tc := tc
		t.Run(fmt.Sprintf("%02d:%02d", tc.h, tc.m), func(t *testing.T) {
			ts := time.Date(2026, 5, 19, tc.h, tc.m, 0, 0, msk)
			got := cbr.AdaptiveInterval(ts)
			if tc.fast && got != 1*time.Second {
				t.Errorf("want 1s, got %v", got)
			}
			if !tc.fast && got != 5*time.Minute {
				t.Errorf("want 5min, got %v", got)
			}
		})
	}
}

// --- Source behaviour ---

func TestSource_Name(t *testing.T) {
	s := cbr.New(zerolog.Nop())
	if s.Name() != "cbr" {
		t.Errorf("expected cbr, got %s", s.Name())
	}
}

func TestSource_UnknownSymbol(t *testing.T) {
	s := cbr.New(zerolog.Nop())
	_, err := s.Subscribe(context.Background(), []string{"UNKNOWN"})
	if err == nil {
		t.Fatal("expected error for unknown symbol")
	}
}

func TestSource_TicksDelivered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, validXML("19.05.2026"))
	}))
	defer srv.Close()

	s := newTestSource(srv)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	ch, err := s.Subscribe(ctx, []string{source.SymbolUSDRubOfficial, source.SymbolEURRubOfficial})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	var ticks []source.Tick
	for tick := range ch {
		ticks = append(ticks, tick)
		if len(ticks) >= 2 {
			break
		}
	}

	if len(ticks) < 2 {
		t.Fatalf("expected at least 2 ticks, got %d", len(ticks))
	}

	seen := make(map[string]float64)
	for _, tk := range ticks {
		seen[tk.Symbol] = tk.Price
		if tk.Kind != source.KindOfficialRate {
			t.Errorf("expected KindOfficialRate, got %v", tk.Kind)
		}
		if tk.Source != "cbr" {
			t.Errorf("expected source=cbr, got %s", tk.Source)
		}
	}
	if seen[source.SymbolUSDRubOfficial] != 82.5 {
		t.Errorf("USD: want 82.5, got %v", seen[source.SymbolUSDRubOfficial])
	}
	if seen[source.SymbolEURRubOfficial] != 90.3 {
		t.Errorf("EUR: want 90.3, got %v", seen[source.SymbolEURRubOfficial])
	}
}

func TestSource_NoTicksOnSameDate(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, validXML("19.05.2026")) // date never changes
	}))
	defer srv.Close()

	s := newTestSource(srv)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	ch, err := s.Subscribe(ctx, []string{source.SymbolUSDRubOfficial})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	var ticks []source.Tick
	for tick := range ch {
		ticks = append(ticks, tick)
	}

	// Only 1 tick (first poll). Subsequent polls with same date must not emit.
	if len(ticks) != 1 {
		t.Errorf("expected exactly 1 tick (first poll only), got %d", len(ticks))
	}
	if atomic.LoadInt32(&callCount) < 2 {
		t.Error("expected at least 2 HTTP calls to verify deduplication by date")
	}
}

func TestSource_OnNewPublication_FiredOnDateChange(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		date := "19.05.2026"
		if n >= 2 {
			date = "20.05.2026" // date changes on second call
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, validXML(date))
	}))
	defer srv.Close()

	s := newTestSource(srv)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	ch, err := s.Subscribe(ctx, []string{source.SymbolUSDRubOfficial})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	// drain ticks
	go func() {
		for range ch {
		}
	}()

	select {
	case pubTime := <-s.OnNewPublication:
		if pubTime.IsZero() {
			t.Error("expected non-zero publication time")
		}
	case <-ctx.Done():
		t.Fatal("OnNewPublication not signalled within timeout")
	}
}

func TestSource_OnNewPublication_NotFiredOnFirstPoll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, validXML("19.05.2026"))
	}))
	defer srv.Close()

	s := newTestSource(srv)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	ch, err := s.Subscribe(ctx, []string{source.SymbolUSDRubOfficial})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	go func() {
		for range ch {
		}
	}()

	select {
	case <-s.OnNewPublication:
		t.Error("OnNewPublication must NOT fire on first poll")
	case <-ctx.Done():
		// correct — no publication event on first poll
	}
}

// TestSource_OnNewPublication_NotFiredOnColdStartFutureDate — регрессия на баг,
// из-за которого журнал засорялся фантомными публикациями. На холодном старте
// (рестарт/редеплой) ЦБ почти всегда отдаёт курс уже на СЛЕДУЮЩИЙ рабочий день
// (будущая дата). Раньше это ошибочно поднимало OnNewPublication со временем
// рестарта, и в журнал попадала «публикация» в полночь или в выходной. Теперь
// первый опрос публикацией не считается никогда, но курс всё равно доставляется
// движку как новый официальный (KindNewOfficialRate) для пересчёта фандинга.
func TestSource_OnNewPublication_NotFiredOnColdStartFutureDate(t *testing.T) {
	future := time.Now().AddDate(0, 0, 2).Format("02.01.2006")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, validXML(future))
	}))
	defer srv.Close()

	s := newTestSource(srv)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	ch, err := s.Subscribe(ctx, []string{source.SymbolUSDRubOfficial})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Курс доставлен как новый официальный, несмотря на холодный старт.
	select {
	case tick := <-ch:
		if tick.Kind != source.KindNewOfficialRate {
			t.Errorf("холодный старт с будущей датой: ожидался KindNewOfficialRate, получен %v", tick.Kind)
		}
	case <-ctx.Done():
		t.Fatal("тик не доставлен")
	}

	// Но публикация НЕ сигналится — мы не видели смену даты вживую.
	select {
	case <-s.OnNewPublication:
		t.Error("OnNewPublication не должен срабатывать на холодном старте (даже при будущей дате)")
	case <-ctx.Done():
		// корректно — фантомной публикации нет
	}
}

// TestSource_LogsDetectionAtWarn проверяет, что обнаружение новой публикации курсов
// логируется на уровне Warn — иначе при проде с LOG_LEVEL=warn событие не видно,
// и нельзя диагностировать задержку «публикация→детект».
func TestSource_LogsDetectionAtWarn(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		date := "19.05.2026"
		if n >= 2 {
			date = "20.05.2026" // дата меняется → новая публикация
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, validXML(date))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	logger := zerolog.New(&buf).Level(zerolog.WarnLevel)
	s := cbr.NewWithURL(srv.URL, fastInterval, logger)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	ch, err := s.Subscribe(ctx, []string{source.SymbolUSDRubOfficial})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	go func() {
		for range ch {
		}
	}()

	select {
	case <-s.OnNewPublication:
	case <-ctx.Done():
		t.Fatal("OnNewPublication not signalled within timeout")
	}
	s.Close() // остановить опрос перед чтением буфера

	out := buf.String()
	if !strings.Contains(out, "cbr rates emitted") {
		t.Errorf("ожидался лог детекта на уровне Warn, получено: %q", out)
	}
}

func TestSource_Windows1251Decoded(t *testing.T) {
	// Build a windows-1251 encoded XML with Cyrillic text in <Name>.
	utf8Body := `<?xml version="1.0" encoding="windows-1251"?>
<ValCurs Date="19.05.2026" name="Foreign Currency Market">
<Valute ID="R01235"><CharCode>USD</CharCode><Nominal>1</Nominal><Name>` + "Доллар США" + `</Name><Value>83,0000</Value></Valute>
</ValCurs>`

	enc := charmap.Windows1251.NewEncoder()
	win1251Body, err := enc.Bytes([]byte(utf8Body))
	if err != nil {
		t.Fatalf("encode to windows-1251: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=windows-1251")
		w.Write(win1251Body)
	}))
	defer srv.Close()

	s := newTestSource(srv)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	ch, err := s.Subscribe(ctx, []string{source.SymbolUSDRubOfficial})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	select {
	case tick := <-ch:
		if tick.Price != 83.0 {
			t.Errorf("expected price 83.0, got %v", tick.Price)
		}
	case <-ctx.Done():
		t.Fatal("no tick received for windows-1251 encoded response")
	}

}

func TestSource_ErrorResilienceContinues(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, validXML("19.05.2026"))
	}))
	defer srv.Close()

	s := newTestSource(srv)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	ch, err := s.Subscribe(ctx, []string{source.SymbolUSDRubOfficial})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	select {
	case tick := <-ch:
		if tick.Price == 0 {
			t.Error("price must not be zero")
		}
	case <-ctx.Done():
		t.Fatal("no tick after error recovery")
	}
}

func TestSource_CloseStopsChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, validXML("19.05.2026"))
	}))
	defer srv.Close()

	s := newTestSource(srv)
	ch, err := s.Subscribe(context.Background(), []string{source.SymbolUSDRubOfficial})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	<-ch // first tick
	s.Close()

	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("channel not closed after Close()")
	}
}
