package moexiss

import (
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
