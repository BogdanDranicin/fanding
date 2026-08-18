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
