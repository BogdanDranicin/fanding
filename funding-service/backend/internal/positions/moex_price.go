package positions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type moexMarketData struct {
	MarketData struct {
		Columns []string `json:"columns"`
		Data    [][]any  `json:"data"`
	} `json:"marketdata"`
}

// FetchMOEXLastPrice возвращает последнюю цену инструмента с MOEX ISS.
// board — биржевая доска (TQBR, RFUD и т.д.).
func FetchMOEXLastPrice(ctx context.Context, client *http.Client, symbol, board string) (float64, error) {
	url := moexURL(symbol, board)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	q := req.URL.Query()
	q.Set("iss.only", "marketdata")
	q.Set("marketdata.columns", "SECID,LAST")
	q.Set("iss.json", "extended")
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

	// extended format: [{...}, {"marketdata": {"columns": [...], "data": [...]}}]
	var rows []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return 0, fmt.Errorf("decode: %w", err)
	}
	for _, row := range rows {
		var md moexMarketData
		if err := json.Unmarshal(row, &md); err != nil {
			continue
		}
		if len(md.MarketData.Data) == 0 {
			continue
		}
		lastIdx := -1
		for i, col := range md.MarketData.Columns {
			if col == "LAST" {
				lastIdx = i
				break
			}
		}
		if lastIdx < 0 {
			continue
		}
		for _, row := range md.MarketData.Data {
			if lastIdx >= len(row) {
				continue
			}
			if row[lastIdx] == nil {
				continue
			}
			if v, ok := row[lastIdx].(float64); ok && v > 0 {
				return v, nil
			}
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
