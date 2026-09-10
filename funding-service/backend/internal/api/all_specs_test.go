package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAllSpecsRefreshKeepsMarketWhenOneLegFails(t *testing.T) {
	forts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"securities":{"columns":["SECID","SHORTNAME","INITIALMARGIN","LOTVOLUME","STEPPRICE","MINSTEP"],
			"data":[["USDRUBF","USDRUBF",12835.07,1000,10,0.01]]}}`)
	}))
	defer forts.Close()

	tqbrUp := true
	tqbr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !tqbrUp {
			http.Error(w, "iss down", http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"securities":{"columns":["SECID","SHORTNAME","LOTSIZE","MINSTEP"],
			"data":[["GAZP","ГАЗПРОМ ао",10,0.01]]}}`)
	}))
	defer tqbr.Close()

	c := &allSpecsCache{httpClient: &http.Client{}}
	if err := c.refreshFrom(context.Background(), forts.URL, tqbr.URL); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if got := symbolsOf(t, c); len(got) != 2 {
		t.Fatalf("first refresh gave %v", got)
	}

	tqbrUp = false
	if err := c.refreshFrom(context.Background(), forts.URL, tqbr.URL); err == nil {
		t.Error("want error reported for the failed TQBR leg")
	}
	got := symbolsOf(t, c)
	if !got["GAZP"] {
		t.Errorf("stocks dropped from the instrument list after a failed TQBR leg: %v", got)
	}
	if !got["USDRUBF"] {
		t.Errorf("futures lost: %v", got)
	}
}

func symbolsOf(t *testing.T, c *allSpecsCache) map[string]bool {
	t.Helper()
	data, err := c.get()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	out := make(map[string]bool, len(data))
	for _, inst := range data {
		out[inst.Symbol] = true
	}
	return out
}
