package moexiss_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
