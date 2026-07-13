package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetchMoexSwapRates verifies the SWAPRATE parse: perpetuals (numeric SWAPRATE,
// including a genuine 0) are kept; quarterly futures (null SWAPRATE) are dropped.
func TestFetchMoexSwapRates(t *testing.T) {
	const body = `{"marketdata":{"columns":["SECID","SWAPRATE"],"data":[
		["GAZPF",0.04174],
		["CNYRUBF",0.00399],
		["USDRUBF",0.0],
		["SiU6",null],
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
		t.Error("quarterly future SiU6 (null SWAPRATE) must be dropped")
	}
	if _, ok := got["CRU6"]; ok {
		t.Error("quarterly future CRU6 (null SWAPRATE) must be dropped")
	}
}
