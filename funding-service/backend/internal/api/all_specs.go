package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

//go:embed data/instruments_fallback.json
var instrumentsFallbackJSON []byte

const (
	allSpecsTTL   = 4 * time.Hour
	fetchTimeout  = 20 * time.Second
	retryInterval = 30 * time.Second
)

// InstrumentInfo holds static contract parameters for a single instrument.
type InstrumentInfo struct {
	Symbol        string  `json:"symbol"`
	ShortName     string  `json:"short_name"`
	MarketType    string  `json:"market_type"` // "future" | "stock"
	InitialMargin float64 `json:"initial_margin"`
	LotSize       float64 `json:"lot_size"`
	StepPrice     float64 `json:"step_price"`
	MinStep       float64 `json:"min_step"`
}

type allSpecsCache struct {
	mu         sync.RWMutex
	data       []InstrumentInfo
	fetchedAt  time.Time
	httpClient *http.Client
}

var globalAllSpecs = &allSpecsCache{
	httpClient: &http.Client{
		Timeout: fetchTimeout,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			TLSHandshakeTimeout: 8 * time.Second,
		},
	},
}

// startWarmup launches a background goroutine that fills the cache on startup
// (retrying every 30s until success) then refreshes every allSpecsTTL.
// Falls back to the embedded snapshot if MOEX is unreachable.
func (c *allSpecsCache) startWarmup() {
	// Load embedded fallback immediately so the endpoint is usable right away.
	if len(instrumentsFallbackJSON) > 0 {
		var fallback []InstrumentInfo
		if err := json.Unmarshal(instrumentsFallbackJSON, &fallback); err == nil && len(fallback) > 0 {
			c.mu.Lock()
			if len(c.data) == 0 {
				c.data = fallback
				// Don't set fetchedAt so live data replaces it as soon as it loads.
			}
			c.mu.Unlock()
		}
	}

	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
			err := c.refresh(ctx)
			cancel()
			if err == nil {
				break
			}
			time.Sleep(retryInterval)
		}
		ticker := time.NewTicker(allSpecsTTL)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
			c.refresh(ctx) //nolint:errcheck // stale data is fine on refresh error
			cancel()
		}
	}()
}

func (c *allSpecsCache) refresh(ctx context.Context) error {
	futures, err := c.fetchFORTS(ctx)
	if err != nil {
		return err
	}
	stocks, _ := c.fetchTQBR(ctx)

	result := make([]InstrumentInfo, 0, len(futures)+len(stocks))
	result = append(result, futures...)
	result = append(result, stocks...)

	c.mu.Lock()
	c.data = result
	c.fetchedAt = time.Now()
	c.mu.Unlock()
	return nil
}

// get returns cached data immediately (from live fetch or embedded fallback).
func (c *allSpecsCache) get() ([]InstrumentInfo, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.data) == 0 {
		return nil, errors.New("instruments not yet loaded")
	}
	return c.data, nil
}

func handleAllSpecs(w http.ResponseWriter, r *http.Request) {
	data, err := globalAllSpecs.get()
	if err != nil {
		w.Header().Set("Retry-After", "3")
		http.Error(w, "instruments loading, please retry", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

type moexISSResp struct {
	Securities struct {
		Columns []string `json:"columns"`
		Data    [][]any  `json:"data"`
	} `json:"securities"`
}

func (c *allSpecsCache) fetchFORTS(ctx context.Context) ([]InstrumentInfo, error) {
	const url = "https://iss.moex.com/iss/engines/futures/markets/forts/securities.json" +
		"?iss.meta=off&iss.only=securities" +
		"&securities.columns=SECID,SHORTNAME,INITIALMARGIN,LOTVOLUME,STEPPRICE,MINSTEP"
	return c.fetch(ctx, url, "future", "LOTVOLUME")
}

func (c *allSpecsCache) fetchTQBR(ctx context.Context) ([]InstrumentInfo, error) {
	const url = "https://iss.moex.com/iss/engines/stock/markets/shares/boards/TQBR/securities.json" +
		"?iss.meta=off&iss.only=securities" +
		"&securities.columns=SECID,SHORTNAME,LOTSIZE,MINSTEP"
	return c.fetch(ctx, url, "stock", "LOTSIZE")
}

func (c *allSpecsCache) fetch(ctx context.Context, url, marketType, lotField string) ([]InstrumentInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("moex iss: status " + resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var raw moexISSResp
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	idx := make(map[string]int, len(raw.Securities.Columns))
	for i, col := range raw.Securities.Columns {
		idx[col] = i
	}

	result := make([]InstrumentInfo, 0, len(raw.Securities.Data))
	for _, row := range raw.Securities.Data {
		sym, _ := strAt(row, idx, "SECID")
		if sym == "" {
			continue
		}
		name, _ := strAt(row, idx, "SHORTNAME")
		info := InstrumentInfo{
			Symbol:        sym,
			ShortName:     name,
			MarketType:    marketType,
			InitialMargin: floatAt(row, idx, "INITIALMARGIN"),
			LotSize:       floatAt(row, idx, lotField),
			StepPrice:     floatAt(row, idx, "STEPPRICE"),
			MinStep:       floatAt(row, idx, "MINSTEP"),
		}
		result = append(result, info)
	}
	return result, nil
}

func strAt(row []any, idx map[string]int, col string) (string, bool) {
	i, ok := idx[col]
	if !ok || i >= len(row) {
		return "", false
	}
	s, ok := row[i].(string)
	return s, ok
}

func floatAt(row []any, idx map[string]int, col string) float64 {
	i, ok := idx[col]
	if !ok || i >= len(row) {
		return 0
	}
	v, _ := row[i].(float64)
	return v
}
