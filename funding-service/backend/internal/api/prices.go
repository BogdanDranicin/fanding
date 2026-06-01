package api

import (
	"context"
	"encoding/json"
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

	idx := make(map[string]int, len(raw.Marketdata.Columns))
	for i, col := range raw.Marketdata.Columns {
		idx[col] = i
	}

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
