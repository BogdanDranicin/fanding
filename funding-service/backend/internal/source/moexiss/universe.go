package moexiss

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Security — строка справочника инструментов режима торгов.
type Security struct {
	SecID string
	// SecType — тип бумаги по классификации биржи: «1» обыкновенная акция,
	// «2» привилегированная, «D» депозитарная расписка, «J» пай биржевого фонда,
	// «A»/«B»/«9» — облигации и паи закрытых фондов, торгуемые тем же режимом.
	SecType string
	// ShortName — человеческое имя бумаги: «Аэрофлот», «Si-9.26».
	ShortName string
	// LotSize — сколько бумаг в лоте (у фьючерсов — LOTVOLUME).
	LotSize int
	// MinStep — шаг цены.
	MinStep float64
	// Decimals — сколько знаков после запятой биржа держит в цене.
	Decimals int
}

// FetchBoardSecurities отдаёт справочник инструментов режима торгов.
//
// Нужен потому, что «все акции TQBR» — это не только акции: в том же режиме
// торгуются 113 паёв БПИФ и сотня облигаций, у которых маркетмейкер весь день
// печатает один и тот же лот через равные промежутки. Формально это робот, по
// сути — шум, забивающий страницу.
func (c *Client) FetchBoardSecurities(ctx context.Context, feed TradeFeed) ([]Security, error) {
	if feed.Engine == "" || feed.Market == "" {
		return nil, fmt.Errorf("securities list needs an engine and a market")
	}
	// Борд необязателен: у срочного рынка лента адресуется рынком целиком, и
	// справочник там читается тем же адресом без борда.
	scope := ""
	if feed.Board != "" {
		scope = "/boards/" + feed.Board
	}
	// LOTSIZE у акций и LOTVOLUME у фьючерсов — одно и то же поле под разными
	// именами; просим оба, дальше берём то, которое пришло.
	url := fmt.Sprintf("%s/engines/%s/markets/%s%s/securities.json"+
		"?iss.meta=off&iss.only=securities"+
		"&securities.columns=SECID,SECTYPE,SHORTNAME,LOTSIZE,LOTVOLUME,MINSTEP,DECIMALS",
		c.baseURL, feed.Engine, feed.Market, scope)

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

	var body struct {
		Securities struct {
			Columns []string        `json:"columns"`
			Data    [][]interface{} `json:"data"`
		} `json:"securities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}

	idx := map[string]int{}
	for i, col := range body.Securities.Columns {
		idx[col] = i
	}
	secid, okID := idx["SECID"]
	sectype, okType := idx["SECTYPE"]
	if !okID || !okType {
		return nil, fmt.Errorf("securities list has no SECID/SECTYPE columns")
	}

	text := func(row []interface{}, col string) string {
		i, ok := idx[col]
		if !ok || i >= len(row) {
			return ""
		}
		v, _ := row[i].(string)
		return v
	}
	number := func(row []interface{}, cols ...string) float64 {
		for _, col := range cols {
			i, ok := idx[col]
			if !ok || i >= len(row) {
				continue
			}
			if v, ok := row[i].(float64); ok {
				return v
			}
		}
		return 0
	}

	out := make([]Security, 0, len(body.Securities.Data))
	for _, row := range body.Securities.Data {
		if len(row) <= secid || len(row) <= sectype {
			continue
		}
		id, _ := row[secid].(string)
		typ, _ := row[sectype].(string)
		if id == "" {
			continue
		}
		out = append(out, Security{
			SecID:     strings.ToUpper(id),
			SecType:   typ,
			ShortName: text(row, "SHORTNAME"),
			LotSize:   int(number(row, "LOTSIZE", "LOTVOLUME")),
			MinStep:   number(row, "MINSTEP"),
			Decimals:  int(number(row, "DECIMALS")),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("securities list for %s is empty", feed.describe())
	}
	return out, nil
}
