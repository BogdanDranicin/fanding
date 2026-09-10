package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetchMoexSwapRates verifies the SWAPRATE parse: contracts are selected by the
// perpetual LASTTRADEDATE sentinel, so a perpetual with a genuine 0 rate is kept while
// quarterly futures are dropped whether ISS reports their SWAPRATE as null (old
// behaviour) or as 0.0 (what it actually returns now — the bug this guards).
func TestFetchMoexSwapRates(t *testing.T) {
	const body = `{
		"securities":{"columns":["SECID","LASTTRADEDATE"],"data":[
			["GAZPF","2100-01-01"],
			["CNYRUBF","2100-01-01"],
			["USDRUBF","2100-01-01"],
			["SiU6","2026-09-18"],
			["CRU6","2026-09-18"]
		]},
		"marketdata":{"columns":["SECID","SWAPRATE"],"data":[
			["GAZPF",0.04174],
			["CNYRUBF",0.00399],
			["USDRUBF",0.0],
			["SiU6",0.0],
			["CRU6",null]
		]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	got, err := fetchMoexSwapRates(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchMoexSwapRates: %v", err)
	}

	want := map[string]float64{"GAZPF": 0.04174, "CNYRUBF": 0.00399, "USDRUBF": 0.0}
	if len(got) != len(want) {
		t.Fatalf("got %d rates %v, want %d %v", len(got), got, len(want), want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if _, ok := got["SiU6"]; ok {
		t.Error("quarterly future SiU6 (SWAPRATE 0.0, real expiry) must be dropped")
	}
	if _, ok := got["CRU6"]; ok {
		t.Error("quarterly future CRU6 (null SWAPRATE) must be dropped")
	}
}

// TestFetchMoexSwapRatesNoPerpetuals checks that a moved LASTTRADEDATE sentinel is
// reported as an error, so the handler keeps serving the last known rates instead of
// wiping funding off every position.
func TestFetchMoexSwapRatesNoPerpetuals(t *testing.T) {
	const body = `{
		"securities":{"columns":["SECID","LASTTRADEDATE"],"data":[
			["GAZPF","2200-01-01"],
			["SiU6","2026-09-18"]
		]},
		"marketdata":{"columns":["SECID","SWAPRATE"],"data":[
			["GAZPF",0.04174],
			["SiU6",0.0]
		]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	if got, err := fetchMoexSwapRates(context.Background(), srv.URL); err == nil {
		t.Fatalf("want error when no contract matches the sentinel, got rates %v", got)
	}
}

func TestFetchMoexPricesFallsBackToClosePrice(t *testing.T) {
	const body = `{"marketdata":{"columns":["SECID","LAST","SETTLEPRICE","LCLOSEPRICE"],"data":[
		["GAZP",93.0,null,92.4],
		["AMEZ",null,null,68.05],
		["USDRUBF",null,84.5,null],
		["DEAD",null,null,null]
	]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	got, err := fetchMoexPrices(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchMoexPrices: %v", err)
	}
	want := map[string]float64{"GAZP": 93.0, "AMEZ": 68.05, "USDRUBF": 84.5}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for sym, price := range want {
		if got[sym] != price {
			t.Errorf("%s = %v, want %v", sym, got[sym], price)
		}
	}
}

func TestPricesRefreshKeepsMarketWhenOneLegFails(t *testing.T) {
	forts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"marketdata":{"columns":["SECID","LAST"],"data":[["USDRUBF",84.19]]}}`)
	}))
	defer forts.Close()

	tqbrUp := true
	tqbr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !tqbrUp {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("response writer is not a Hijacker")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			conn.Close()
			return
		}
		fmt.Fprint(w, `{"marketdata":{"columns":["SECID","LAST"],"data":[["GAZP",93.0]]}}`)
	}))
	defer tqbr.Close()

	c := &marketPricesCache{}
	if err := c.refreshFrom(context.Background(), forts.URL, tqbr.URL); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if data, _ := c.get(); data["GAZP"] != 93.0 || data["USDRUBF"] != 84.19 {
		t.Fatalf("first refresh gave %v", data)
	}

	tqbrUp = false
	if err := c.refreshFrom(context.Background(), forts.URL, tqbr.URL); err == nil {
		t.Error("want error reported for the failed TQBR leg")
	}
	data, _ := c.get()
	if data["GAZP"] != 93.0 {
		t.Errorf("stock prices dropped after a failed TQBR leg: %v", data)
	}
	if data["USDRUBF"] != 84.19 {
		t.Errorf("futures prices lost: %v", data)
	}
}
