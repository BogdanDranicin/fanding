package positions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type moexResponse struct {
	MarketData struct {
		Columns []string `json:"columns"`
		Data    [][]any  `json:"data"`
	} `json:"marketdata"`
}

// FetchMOEXLastPrice возвращает последнюю цену инструмента с MOEX ISS.
// board — биржевая доска (TQBR, RFUD и т.д.).
func FetchMOEXLastPrice(ctx context.Context, client *http.Client, symbol, board string) (float64, error) {
	rawURL := moexURL(symbol, board)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	q := req.URL.Query()
	q.Set("iss.only", "marketdata")
	q.Set("marketdata.columns", "SECID,LAST")
	q.Set("iss.meta", "off")
	req.URL.RawQuery = q.Encode()

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("moex request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("moex returned %d", resp.StatusCode)
	}

	var result moexResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode: %w", err)
	}

	lastIdx := -1
	for i, col := range result.MarketData.Columns {
		if col == "LAST" {
			lastIdx = i
			break
		}
	}
	if lastIdx < 0 {
		return 0, fmt.Errorf("no LAST column for %s", symbol)
	}
	for _, row := range result.MarketData.Data {
		if lastIdx >= len(row) || row[lastIdx] == nil {
			continue
		}
		if v, ok := row[lastIdx].(float64); ok && v > 0 {
			return v, nil
		}
	}
	return 0, fmt.Errorf("no LAST price for %s", symbol)
}

func moexURL(symbol, board string) string {
	switch board {
	case "RFUD", "SPBFUT", "TQBR_FUTURES":
		return fmt.Sprintf(
			"https://iss.moex.com/iss/engines/futures/markets/forts/boards/%s/securities/%s.json",
			board, symbol)
	default:
		return fmt.Sprintf(
			"https://iss.moex.com/iss/engines/stock/markets/shares/boards/%s/securities/%s.json",
			board, symbol)
	}
}
