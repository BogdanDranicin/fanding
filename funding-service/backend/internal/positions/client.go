package positions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	defaultAuthBase = "https://id-api.tradersdiaries.com"
	defaultAPIBase  = "https://api.tradersdiaries.com"
)

// Position представляет одну активную брокерскую позицию.
type Position struct {
	Symbol     string   `json:"symbol"`
	Exchange   string   `json:"exchange"`
	Board      string   `json:"board"`
	Side       string   `json:"side"`
	Pos        int      `json:"pos"`
	TotalBuy   float64  `json:"total_buy"`
	TotalSell  float64  `json:"total_sell"`
	Profit     *float64 `json:"profit"`
	ProfitPerc *float64 `json:"profit_perc"`
	Date       string   `json:"date"`
	Time       string   `json:"time"`
	Asset      string   `json:"asset"`
}

// Client выполняет запросы к tradersdiaries.com API.
type Client struct {
	authBase string
	apiBase  string
	http     *http.Client
}

// New создаёт Client с production URL'ами.
func New() *Client {
	return NewClientWithURLs(defaultAuthBase, defaultAPIBase)
}

// NewClientWithURLs создаёт Client с кастомными URL для тестирования.
func NewClientWithURLs(authBase, apiBase string) *Client {
	return &Client{
		authBase: authBase,
		apiBase:  apiBase,
		http:     &http.Client{Timeout: 10 * time.Second},
	}
}

type silentResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

// GetAccessToken вызывает silent endpoint и возвращает accessToken.
func (c *Client) GetAccessToken(ctx context.Context, ssoSession, deviceID string) (string, error) {
	url := fmt.Sprintf("%s/client/tradersdiaries/oauth/v2/silent?device_id=%s", c.authBase, deviceID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Cookie", "sso_session="+ssoSession)
	req.Header.Set("Origin", "https://tradersdiaries.com")
	req.Header.Set("Referer", "https://tradersdiaries.com/")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("silent endpoint returned %d", resp.StatusCode)
	}

	var sr silentResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if sr.AccessToken == "" {
		return "", fmt.Errorf("empty accessToken in response")
	}
	return sr.AccessToken, nil
}

var positionsBody = []byte(`{"filter":{},"sort":[{"col":"symbol","asc":true}]}`)

// GetPositions вызывает get-positions endpoint и возвращает активные позиции.
// При EOF (сброс соединения сервером) делает один повтор.
func (c *Client) GetPositions(ctx context.Context, accessToken string) ([]Position, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		pos, err := c.doGetPositions(ctx, accessToken)
		if err == nil {
			return pos, nil
		}
		lastErr = err
		if !isEOF(err) {
			break
		}
	}
	return nil, lastErr
}

func (c *Client) doGetPositions(ctx context.Context, accessToken string) ([]Position, error) {
	url := fmt.Sprintf("%s/prod/prop/get-positions", c.apiBase)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(positionsBody))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Authorization", accessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get-positions returned %d", resp.StatusCode)
	}

	var pos []Position
	if err := json.NewDecoder(resp.Body).Decode(&pos); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return pos, nil
}

func isEOF(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "EOF") || strings.Contains(msg, "connection reset")
}
