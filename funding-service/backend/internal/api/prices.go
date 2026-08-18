package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

const pricesTTL = 60 * time.Second

type pricesCache struct {
	mu        sync.RWMutex
	data      map[string]float64
	fetchedAt time.Time
}

var globalPrices = &pricesCache{}

func (c *pricesCache) get() (map[string]float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.data) == 0 {
		return nil, false
	}
	return c.data, time.Since(c.fetchedAt) <= pricesTTL
}

func (c *pricesCache) refresh(ctx context.Context) error {
	futures, err := fetchMoexPrices(ctx,
		"https://iss.moex.com/iss/engines/futures/markets/forts/securities.json"+
			"?iss.meta=off&iss.only=marketdata&marketdata.columns=SECID,LAST,SETTLEPRICE,PREVPRICE",
	)
	if err != nil {
		return err
	}
	stocks, _ := fetchMoexPrices(ctx,
		"https://iss.moex.com/iss/engines/stock/markets/shares/boards/TQBR/securities.json"+
			"?iss.meta=off&iss.only=marketdata&marketdata.columns=SECID,LAST,PREVPRICE",
	)

	result := make(map[string]float64, len(futures)+len(stocks))
	for k, v := range futures {
		result[k] = v
	}
	for k, v := range stocks {
		result[k] = v
	}

	c.mu.Lock()
	c.data = result
	c.fetchedAt = time.Now()
	c.mu.Unlock()
	return nil
}

type moexMDResp struct {
	Marketdata struct {
		Columns []string `json:"columns"`
		Data    [][]any  `json:"data"`
	} `json:"marketdata"`
}

func fetchMoexPrices(ctx context.Context, url string) (map[string]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := globalAllSpecs.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var raw moexMDResp
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	idx := columnIndex(raw.Marketdata.Columns)

	result := make(map[string]float64, len(raw.Marketdata.Data))
	for _, row := range raw.Marketdata.Data {
		sym, _ := strAt(row, idx, "SECID")
		if sym == "" {
			continue
		}
		price := floatAt(row, idx, "LAST")
		if price == 0 {
			price = floatAt(row, idx, "SETTLEPRICE")
		}
		if price == 0 {
			price = floatAt(row, idx, "PREVPRICE")
		}
		if price > 0 {
			result[sym] = price
		}
	}
	return result, nil
}

func handlePrices(w http.ResponseWriter, r *http.Request) {
	data, fresh := globalPrices.get()
	if fresh {
		writeJSON(w, http.StatusOK, data)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), fetchTimeout)
	defer cancel()
	if err := globalPrices.refresh(ctx); err != nil {
		if data != nil {
			writeJSON(w, http.StatusOK, data)
			return
		}
		w.Header().Set("Retry-After", "10")
		http.Error(w, "prices unavailable", http.StatusServiceUnavailable)
		return
	}
	data, _ = globalPrices.get()
	writeJSON(w, http.StatusOK, data)
}

// ─── perpetual funding rates (MOEX SWAPRATE) ─────────────────────────────────
//
// Served from the backend (not fetched in the browser) because the site's CSP
// only allows connect-src 'self' — a direct browser call to iss.moex.com is
// blocked. Same 60 s TTL and fallback-to-stale behaviour as prices.

var globalSwapRates = &pricesCache{}

func (c *pricesCache) refreshSwapRates(ctx context.Context) error {
	// Both ISS blocks in one request: `securities` says which contract is perpetual,
	// `marketdata` carries the rate. See fetchMoexSwapRates for why both are needed.
	rates, err := fetchMoexSwapRates(ctx,
		"https://iss.moex.com/iss/engines/futures/markets/forts/securities.json"+
			"?iss.meta=off&iss.only=securities,marketdata"+
			"&securities.columns=SECID,LASTTRADEDATE&marketdata.columns=SECID,SWAPRATE",
	)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.data = rates
	c.fetchedAt = time.Now()
	c.mu.Unlock()
	return nil
}

// moexSwapResp carries both ISS blocks the SWAPRATE query needs.
type moexSwapResp struct {
	moexISSResp // securities: SECID, LASTTRADEDATE
	moexMDResp  // marketdata: SECID, SWAPRATE
}

// perpLastTradeDate is the sentinel expiry MOEX gives perpetual futures — they never
// expire, so LASTTRADEDATE is pinned far in the future. Quarterly contracts carry a
// real date ("2026-08-28"), which is what tells the two kinds apart.
const perpLastTradeDate = "2100-01-01"

// fetchMoexSwapRates returns SECID→SWAPRATE for every perpetual future.
//
// Perpetuals are selected by LASTTRADEDATE, not by SWAPRATE being non-null: ISS now
// reports SWAPRATE 0.0 (not null) for quarterly futures, so filtering on null let all
// ~460 quarterlies through with a rate of 0 and the calculator showed them a funding
// row of 0 ₽ instead of a dash. A genuine 0 on a perpetual is still kept.
func fetchMoexSwapRates(ctx context.Context, url string) (map[string]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := globalAllSpecs.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var raw moexSwapResp
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	secIdx := columnIndex(raw.Securities.Columns)
	perpetual := make(map[string]struct{}, 16)
	for _, row := range raw.Securities.Data {
		sym, _ := strAt(row, secIdx, "SECID")
		if sym == "" {
			continue
		}
		if d, _ := strAt(row, secIdx, "LASTTRADEDATE"); d == perpLastTradeDate {
			perpetual[sym] = struct{}{}
		}
	}

	// FORTS always lists perpetuals, so an empty set means the sentinel date moved and
	// the filter no longer matches anything. Fail instead of returning an empty map —
	// handleSwapRates then keeps serving the last known rates rather than silently
	// dropping funding from every position.
	if len(perpetual) == 0 {
		return nil, errors.New("moex iss: no perpetual futures matched LASTTRADEDATE " + perpLastTradeDate)
	}

	mdIdx := columnIndex(raw.Marketdata.Columns)
	result := make(map[string]float64, len(perpetual))
	for _, row := range raw.Marketdata.Data {
		sym, _ := strAt(row, mdIdx, "SECID")
		if _, ok := perpetual[sym]; !ok {
			continue
		}
		if v, ok := floatAtOK(row, mdIdx, "SWAPRATE"); ok {
			result[sym] = v
		}
	}
	return result, nil
}

func handleSwapRates(w http.ResponseWriter, r *http.Request) {
	data, fresh := globalSwapRates.get()
	if fresh {
		writeJSON(w, http.StatusOK, data)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), fetchTimeout)
	defer cancel()
	if err := globalSwapRates.refreshSwapRates(ctx); err != nil {
		if data != nil {
			writeJSON(w, http.StatusOK, data)
			return
		}
		w.Header().Set("Retry-After", "10")
		http.Error(w, "swap rates unavailable", http.StatusServiceUnavailable)
		return
	}
	data, _ = globalSwapRates.get()
	writeJSON(w, http.StatusOK, data)
}
