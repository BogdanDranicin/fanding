package moexiss

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// ErrNotModified is returned when the server responds with 304 Not Modified.
var ErrNotModified = errors.New("not modified")

const (
	defaultBaseURL = "https://iss.moex.com/iss"
	requestTimeout = time.Second
	maxRetries     = 3
)

// Client is a low-level MOEX ISS HTTP client with ETag caching and retry.
type Client struct {
	baseURL    string
	httpClient *http.Client
	etags      sync.Map // url -> etag string
}

// NewClient creates a Client against the live MOEX ISS endpoint.
func NewClient() *Client {
	return newClient(defaultBaseURL)
}

// NewClientWithBaseURL creates a Client against a custom base URL (useful in tests).
func NewClientWithBaseURL(baseURL string) *Client {
	return newClient(baseURL)
}

func newClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: requestTimeout,
			Transport: &http.Transport{
				MaxIdleConns:    10,
				IdleConnTimeout: 90 * time.Second,
			},
		},
	}
}

// columnData holds the raw columns+data pair from MOEX ISS JSON.
type columnData struct {
	Columns []string        `json:"columns"`
	Data    [][]interface{} `json:"data"`
}

type issResponse struct {
	Securities columnData `json:"securities"`
	MarketData columnData `json:"marketdata"`
}

// RawResponse holds the parsed marketdata and securities sections from a single MOEX ISS response.
type RawResponse struct {
	// MarketData maps column names to values from data[0].
	MarketData map[string]interface{}
	// Securities maps column names to values from securities.data[0].
	Securities map[string]interface{}
	ETag       string
}

// FetchSecurity fetches current market data for the given security.
// If board is empty the boards segment is omitted from the URL.
// Returns ErrNotModified when the server responds with 304.
// Retries up to maxRetries times with exponential backoff on transient errors.
func (c *Client) FetchSecurity(ctx context.Context, board, market, engine, symbol string) (*RawResponse, error) {
	url := c.buildURL(board, market, engine, symbol)

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(100<<uint(attempt-1)) * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		resp, err := c.doRequest(ctx, url)
		if err == nil || errors.Is(err, ErrNotModified) {
			return resp, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func (c *Client) buildURL(board, market, engine, symbol string) string {
	if board != "" {
		return fmt.Sprintf("%s/engines/%s/markets/%s/boards/%s/securities/%s.json",
			c.baseURL, engine, market, board, symbol)
	}
	return fmt.Sprintf("%s/engines/%s/markets/%s/securities/%s.json",
		c.baseURL, engine, market, symbol)
}

func (c *Client) doRequest(ctx context.Context, url string) (*RawResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	if etag, ok := c.etags.Load(url); ok {
		req.Header.Set("If-None-Match", etag.(string))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil, ErrNotModified
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var raw issResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	md, err := parseColumnData(raw.MarketData)
	if err != nil {
		return nil, fmt.Errorf("parse marketdata: %w", err)
	}

	var sec map[string]interface{}
	if len(raw.Securities.Data) > 0 {
		sec, _ = parseColumnData(raw.Securities)
	}

	etag := resp.Header.Get("ETag")
	if etag != "" {
		c.etags.Store(url, etag)
	}

	return &RawResponse{
		MarketData: md,
		Securities: sec,
		ETag:       etag,
	}, nil
}

// Trade is a single executed deal from the MOEX ISS trades endpoint.
type Trade struct {
	TradeNo   int64
	Price     float64
	Quantity  float64
	Timestamp time.Time // trade moment (TRADEDATE+TRADETIME MSK, falls back to SYSTIME)
	OffMarket bool      // OFFMARKETDEAL != 0 — адресная сделка, исключается из VWAP
	// Backdated: TRADEDATE не совпадает с TRADE_SESSION_DATE — сделка чужого
	// календарного дня, которую биржа приписала к текущей сессии (см. Tick.Backdated).
	Backdated bool
}

type tradesResponse struct {
	Trades columnData `json:"trades"`
}

// tradesMaxPages caps the catch-up pagination loop so a misbehaving server
// (e.g. one that keeps returning the same page) cannot spin us forever.
const tradesMaxPages = 200

// FetchTradesSince returns all trades with TRADENO greater than sinceTradeNo,
// in ascending TRADENO order. It follows ISS pagination (?tradeno=N&next_trade=1)
// until an empty page is returned, so a call with sinceTradeNo=0 backfills the
// whole current session. No ETag caching: each call is an explicit increment.
func (c *Client) FetchTradesSince(ctx context.Context, engine, market, secid string, sinceTradeNo int64) ([]Trade, error) {
	var all []Trade
	last := sinceTradeNo
	for page := 0; page < tradesMaxPages; page++ {
		batch, err := c.fetchTradesPage(ctx, engine, market, secid, last)
		if err != nil {
			// Return what we already have: partial progress is still valid
			// (TRADENO is strictly increasing, the caller resumes from the last one).
			return all, err
		}
		if len(batch) == 0 {
			return all, nil
		}
		maxNo := batch[len(batch)-1].TradeNo
		if maxNo <= last {
			// Server did not advance past our cursor — stop instead of looping.
			return all, fmt.Errorf("trades pagination stuck at tradeno %d", last)
		}
		all = append(all, batch...)
		last = maxNo
	}
	return all, fmt.Errorf("trades pagination exceeded %d pages", tradesMaxPages)
}

func (c *Client) fetchTradesPage(ctx context.Context, engine, market, secid string, sinceTradeNo int64) ([]Trade, error) {
	url := fmt.Sprintf("%s/engines/%s/markets/%s/securities/%s/trades.json?tradeno=%d&next_trade=1",
		c.baseURL, engine, market, secid, sinceTradeNo)

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
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	// UseNumber keeps TRADENO as an exact integer. TRADENO on FORTS is a ~19-digit
	// composite (~2e18) that overflows float64's 53-bit mantissa: consecutive
	// numbers (they increment by 1) would collapse in blocks of ~256, corrupting
	// the ?tradeno=N&next_trade=1 cursor and re-ingesting/duplicating volume.
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var raw tradesResponse
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("unmarshal trades: %w", err)
	}
	return parseTrades(raw.Trades)
}

// parseTrades converts the ISS columns/data table into Trades, sorted as returned
// (ISS emits ascending TRADENO for next_trade=1 requests).
func parseTrades(cd columnData) ([]Trade, error) {
	idx := make(map[string]int, len(cd.Columns))
	for i, col := range cd.Columns {
		idx[col] = i
	}
	for _, required := range []string{"TRADENO", "PRICE", "QUANTITY"} {
		if _, ok := idx[required]; !ok {
			return nil, fmt.Errorf("trades response missing column %s", required)
		}
	}

	trades := make([]Trade, 0, len(cd.Data))
	for _, row := range cd.Data {
		if len(row) != len(cd.Columns) {
			return nil, fmt.Errorf("trades columns/data length mismatch: %d vs %d", len(cd.Columns), len(row))
		}
		cell := func(name string) interface{} {
			i, ok := idx[name]
			if !ok {
				return nil
			}
			return row[i]
		}

		no, ok := numToInt64(cell("TRADENO"))
		if !ok {
			continue
		}
		price, ok := numToFloat64(cell("PRICE"))
		if !ok || price <= 0 {
			continue
		}
		qty, ok := numToFloat64(cell("QUANTITY"))
		if !ok || qty <= 0 {
			continue
		}

		t := Trade{
			TradeNo:  no,
			Price:    price,
			Quantity: qty,
		}
		if v, ok := numToFloat64(cell("OFFMARKETDEAL")); ok && v != 0 {
			t.OffMarket = true
		}
		t.Timestamp = parseTradeTime(cell("TRADEDATE"), cell("TRADETIME"), cell("SYSTIME"))
		t.Backdated = isBackdated(cell("TRADEDATE"), cell("TRADE_SESSION_DATE"))
		if t.Backdated {
			// Момент сделки принадлежит другому дню, а в поток она попала в момент
			// SYSTIME (утренний технический клиринг текущей сессии). Метим её этим
			// моментом: так суточные аккумуляторы движка не сбрасываются чужой датой.
			if ts := parseMSK(cell("SYSTIME")); !ts.IsZero() {
				t.Timestamp = ts
			}
		}
		trades = append(trades, t)
	}
	return trades, nil
}

// numToInt64 extracts an exact int64 from a decoded JSON cell. json.Number
// (from a UseNumber decoder) is parsed losslessly; a plain float64 is accepted
// as a fallback for tests that build column tables by hand.
func numToInt64(v interface{}) (int64, bool) {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i, true
		}
	case float64:
		return int64(x), true
	case int64:
		return x, true
	}
	return 0, false
}

// numToFloat64 extracts a float64 from a decoded JSON cell, handling both
// json.Number (UseNumber decoder) and a plain float64.
func numToFloat64(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		if f, err := x.Float64(); err == nil {
			return f, true
		}
	case float64:
		return x, true
	}
	return 0, false
}

// parseTradeTime derives the trade moment: TRADEDATE+TRADETIME (MSK) preferred,
// SYSTIME as fallback, zero time when neither parses.
func parseTradeTime(dateV, timeV, sysV interface{}) time.Time {
	mskZone := time.FixedZone("MSK", 3*60*60)
	if d, ok := dateV.(string); ok {
		if tm, ok2 := timeV.(string); ok2 {
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", d+" "+tm, mskZone); err == nil {
				return t
			}
		}
	}
	return parseMSK(sysV)
}

// parseMSK parses an ISS "2006-01-02 15:04:05" MSK timestamp cell; zero time on failure.
func parseMSK(v interface{}) time.Time {
	s, ok := v.(string)
	if !ok {
		return time.Time{}
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.FixedZone("MSK", 3*60*60))
	if err != nil {
		return time.Time{}
	}
	return t
}

// isBackdated reports whether the deal was struck on a different calendar day than
// the session it is reported in. Live example (27.07.2026, USDRUBF): 1689 weekend
// deals with TRADEDATE 25–26.07 and TRADETIME 09:59–18:59 arrived at SYSTIME 06:14
// under TRADE_SESSION_DATE=27.07. Counting them as Monday deals dragged the
// 10:00–15:30 VWAP down by 0.0144 (78.10417 instead of 78.11873 — the exchange's
// own minute candles cover exactly the 100673 lots that remain after dropping them).
func isBackdated(dateV, sessionV interface{}) bool {
	d, ok := dateV.(string)
	if !ok || d == "" {
		return false
	}
	s, ok := sessionV.(string)
	if !ok || s == "" {
		return false
	}
	return d != s
}

// parseColumnData zips columns and data[0] into a map.
func parseColumnData(cd columnData) (map[string]interface{}, error) {
	if len(cd.Data) == 0 {
		return nil, errors.New("no data rows in marketdata")
	}
	row := cd.Data[0]
	if len(row) != len(cd.Columns) {
		return nil, fmt.Errorf("columns/data length mismatch: %d vs %d", len(cd.Columns), len(row))
	}
	result := make(map[string]interface{}, len(cd.Columns))
	for i, col := range cd.Columns {
		result[col] = row[i]
	}
	return result, nil
}
