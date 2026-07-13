package moexiss_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/funding-service/backend/internal/source/moexiss"
)

const validISS = `{
  "marketdata": {
    "columns": ["SECID","LAST","BID","OFFER","VOLTODAY","SYSTIME"],
    "data":    [["USDRUBF", 81.91, 81.90, 81.95, 12345, "2024-01-15 10:30:00"]]
  }
}`

func TestFetchSecurity_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(validISS))
	}))
	defer srv.Close()

	client := moexiss.NewClientWithBaseURL(srv.URL)
	resp, err := client.FetchSecurity(context.Background(), "", "forts", "futures", "USDRUBF")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.MarketData["SECID"] != "USDRUBF" {
		t.Errorf("SECID: want USDRUBF, got %v", resp.MarketData["SECID"])
	}
	if resp.MarketData["LAST"] != 81.91 {
		t.Errorf("LAST: want 81.91, got %v", resp.MarketData["LAST"])
	}
	if resp.MarketData["BID"] != 81.90 {
		t.Errorf("BID: want 81.90, got %v", resp.MarketData["BID"])
	}
	if resp.MarketData["OFFER"] != 81.95 {
		t.Errorf("OFFER: want 81.95, got %v", resp.MarketData["OFFER"])
	}
}

func TestFetchSecurity_ETagNotModified(t *testing.T) {
	const tag = `"etag-xyz"`
	callCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Header.Get("If-None-Match") == tag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", tag)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(validISS))
	}))
	defer srv.Close()

	client := moexiss.NewClientWithBaseURL(srv.URL)

	// First call — receives ETag.
	if _, err := client.FetchSecurity(context.Background(), "", "forts", "futures", "USDRUBF"); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Second call — client sends If-None-Match, server replies 304.
	_, err := client.FetchSecurity(context.Background(), "", "forts", "futures", "USDRUBF")
	if !errors.Is(err, moexiss.ErrNotModified) {
		t.Errorf("expected ErrNotModified, got %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 HTTP calls, got %d", callCount)
	}
}

func TestFetchSecurity_BoardInURL(t *testing.T) {
	var receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(validISS))
	}))
	defer srv.Close()

	client := moexiss.NewClientWithBaseURL(srv.URL)
	if _, err := client.FetchSecurity(context.Background(), "RFUD", "forts", "futures", "USDRUBF"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "/engines/futures/markets/forts/boards/RFUD/securities/USDRUBF.json"
	if receivedPath != want {
		t.Errorf("path: want %s, got %s", want, receivedPath)
	}
}

func TestFetchSecurity_NoBoardInURL(t *testing.T) {
	var receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(validISS))
	}))
	defer srv.Close()

	client := moexiss.NewClientWithBaseURL(srv.URL)
	if _, err := client.FetchSecurity(context.Background(), "", "forts", "futures", "USDRUBF"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "/engines/futures/markets/forts/securities/USDRUBF.json"
	if receivedPath != want {
		t.Errorf("path: want %s, got %s", want, receivedPath)
	}
}

// tradesPage builds an ISS trades.json body from (tradeno, price, qty, offmarket) rows.
func tradesPage(rows [][4]float64) string {
	var b strings.Builder
	b.WriteString(`{"trades": {"columns": ["TRADENO","BOARDNAME","SECID","TRADEDATE","TRADETIME","PRICE","QUANTITY","SYSTIME","OFFMARKETDEAL"], "data": [`)
	for i, r := range rows {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `[%d, "RFUD", "USDRUBF", "2026-07-10", "10:30:%02d", %v, %v, "2026-07-10 10:30:59", %d]`,
			int64(r[0]), i, r[1], r[2], int64(r[3]))
	}
	b.WriteString("]}}")
	return b.String()
}

func TestFetchTradesSince_ParsesAndPaginates(t *testing.T) {
	// Server serves two pages then an empty one, keyed by the tradeno cursor.
	pages := map[string]string{
		"0":   tradesPage([][4]float64{{101, 81.90, 5, 0}, {102, 81.95, 3, 0}}),
		"102": tradesPage([][4]float64{{103, 82.00, 7, 1}}),
		"103": tradesPage(nil),
	}
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		if r.URL.Query().Get("next_trade") != "1" {
			t.Errorf("missing next_trade=1 in query: %s", r.URL.RawQuery)
		}
		body, ok := pages[r.URL.Query().Get("tradeno")]
		if !ok {
			t.Errorf("unexpected tradeno cursor %q", r.URL.Query().Get("tradeno"))
			body = tradesPage(nil)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	client := moexiss.NewClientWithBaseURL(srv.URL)
	trades, err := client.FetchTradesSince(context.Background(), "futures", "forts", "USDRUBF", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(trades) != 3 {
		t.Fatalf("want 3 trades across pages, got %d", len(trades))
	}
	if trades[0].TradeNo != 101 || trades[1].TradeNo != 102 || trades[2].TradeNo != 103 {
		t.Errorf("trade numbers out of order: %+v", trades)
	}
	if trades[0].Price != 81.90 || trades[0].Quantity != 5 {
		t.Errorf("trade[0]: want 81.90×5, got %v×%v", trades[0].Price, trades[0].Quantity)
	}
	if trades[0].OffMarket || !trades[2].OffMarket {
		t.Errorf("OFFMARKETDEAL flags wrong: %v %v", trades[0].OffMarket, trades[2].OffMarket)
	}
	wantTS := "2026-07-10 10:30:00"
	if got := trades[0].Timestamp.Format("2006-01-02 15:04:05"); got != wantTS {
		t.Errorf("timestamp: want %s, got %s", wantTS, got)
	}
	if trades[0].Timestamp.Location().String() != "MSK" {
		t.Errorf("timestamp zone: want MSK, got %s", trades[0].Timestamp.Location())
	}

	wantFirst := "/engines/futures/markets/forts/securities/USDRUBF/trades.json?tradeno=0&next_trade=1"
	if paths[0] != wantFirst {
		t.Errorf("first request: want %s, got %s", wantFirst, paths[0])
	}
}

func TestFetchTradesSince_HugeTradeNoExact(t *testing.T) {
	// Real FORTS TRADENO is a ~19-digit composite (~2e18) that overflows float64's
	// 53-bit mantissa. Decoding it through float64 rounds it (…177 -> …176), which
	// corrupts dedup and the next_trade cursor. It must survive as an exact int64.
	const hugeNo int64 = 2023556257914290177
	page := fmt.Sprintf(`{"trades":{"columns":["TRADENO","BOARDNAME","SECID","TRADEDATE","TRADETIME","PRICE","QUANTITY","SYSTIME","OFFMARKETDEAL"],`+
		`"data":[[%d,"RFUD","USDRUBF","2026-07-13","10:30:00",76.73,1,"2026-07-13 10:30:00",0]]}}`, hugeNo)

	var cursors []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("tradeno")
		cursors = append(cursors, cursor)
		w.Header().Set("Content-Type", "application/json")
		if cursor == "0" {
			_, _ = w.Write([]byte(page))
			return
		}
		_, _ = w.Write([]byte(`{"trades":{"columns":["TRADENO","PRICE","QUANTITY"],"data":[]}}`))
	}))
	defer srv.Close()

	client := moexiss.NewClientWithBaseURL(srv.URL)
	trades, err := client.FetchTradesSince(context.Background(), "futures", "forts", "USDRUBF", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(trades) != 1 {
		t.Fatalf("want 1 trade, got %d", len(trades))
	}
	if trades[0].TradeNo != hugeNo {
		t.Errorf("TradeNo corrupted by float64: want %d, got %d (delta %d)",
			hugeNo, trades[0].TradeNo, hugeNo-trades[0].TradeNo)
	}
	// The follow-up page must resume from the EXACT tradeno, not a rounded one.
	wantCursor := fmt.Sprintf("%d", hugeNo)
	found := false
	for _, c := range cursors {
		if c == wantCursor {
			found = true
		}
	}
	if !found {
		t.Errorf("expected follow-up request with exact cursor %s, got %v", wantCursor, cursors)
	}
}

func TestFetchTradesSince_StuckCursorErrors(t *testing.T) {
	// Server always returns the same trade regardless of cursor — the client
	// must detect the non-advancing TRADENO and bail out instead of spinning.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tradesPage([][4]float64{{50, 81.0, 1, 0}})))
	}))
	defer srv.Close()

	client := moexiss.NewClientWithBaseURL(srv.URL)
	_, err := client.FetchTradesSince(context.Background(), "futures", "forts", "USDRUBF", 50)
	if err == nil {
		t.Fatal("expected error for non-advancing tradeno")
	}
}

func TestFetchTradesSince_PartialOnError(t *testing.T) {
	// First page OK, second page 500 — the successfully fetched trades are
	// returned together with the error so the caller can resume from them.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("tradeno") == "0" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tradesPage([][4]float64{{7, 81.5, 2, 0}})))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := moexiss.NewClientWithBaseURL(srv.URL)
	trades, err := client.FetchTradesSince(context.Background(), "futures", "forts", "USDRUBF", 0)
	if err == nil {
		t.Fatal("expected error from failing second page")
	}
	if len(trades) != 1 || trades[0].TradeNo != 7 {
		t.Errorf("want partial result [7], got %+v", trades)
	}
}

func TestFetchSecurity_UnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := moexiss.NewClientWithBaseURL(srv.URL)
	_, err := client.FetchSecurity(context.Background(), "", "forts", "futures", "USDRUBF")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}
