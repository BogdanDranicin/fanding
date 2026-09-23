package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/storage"
)

// Подгрузка цен входа и выхода, которых нет в сообщении автора: цена берётся с
// биржи на момент выхода сообщения. Публичные данные MOEX ISS идут с задержкой
// 15 минут, поэтому задача ждёт, пока момент сообщения станет старше issDelay.

const (
	issBase         = "https://iss.moex.com/iss"
	issDelay        = 16 * time.Minute
	priceFillEvery  = time.Minute
	priceFillMaxTry = 12
)

var msk = time.FixedZone("MSK", 3*60*60)

// errNoInstrument — инструмента нет на бирже (крипта, «?»): ждать бесполезно.
var errNoInstrument = errors.New("инструмент не торгуется на Мосбирже")

// futuresPrefix — общие названия фьючерсов из сообщений («Микс», «Si»): цена
// берётся у ближайшего по сроку контракта, как и в колонке «текущая цена».
var futuresPrefix = map[string]string{
	"MIX": "MX", "RTS": "RI", "Si": "Si", "BR": "BR", "GOLD": "GD", "SILV": "SV", "PLT": "PT", "CNY": "CR",
}

const futuresMonths = "FGHJKMNQUVXZ"

var reOFZNumber = regexp.MustCompile(`^ОФЗ (\d{5})$`)

// priceSource достаёт цену инструмента на момент времени. Всё сетевое — через
// base, чтобы тест мог подставить свой сервер.
type priceSource struct {
	base   string
	client *http.Client
	// markets — коды акций TQBR и фьючерсов FORTS, по ним тикер относится к рынку.
	markets func(ctx context.Context) (tqbr, forts map[string]float64)
}

func newPriceSource() *priceSource {
	return &priceSource{
		base:   issBase,
		client: &http.Client{Timeout: 15 * time.Second},
		markets: func(ctx context.Context) (map[string]float64, map[string]float64) {
			globalPrices.mu.RLock()
			empty := len(globalPrices.lastGoodTQBR) == 0
			globalPrices.mu.RUnlock()
			if empty {
				_ = globalPrices.refresh(ctx)
			}
			globalPrices.mu.RLock()
			defer globalPrices.mu.RUnlock()
			return globalPrices.lastGoodTQBR, globalPrices.lastGoodForts
		},
	}
}

func frontFutures(prefix string, forts map[string]float64) string {
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `([` + futuresMonths + `])([0-9])$`)
	best, bestKey := "", 1<<30
	for code := range forts {
		m := re.FindStringSubmatch(code)
		if m == nil {
			continue
		}
		key := int(m[2][0]-'0')*12 + strings.IndexByte(futuresMonths, m[1][0])
		if key < bestKey {
			best, bestKey = code, key
		}
	}
	return best
}

// candlesPath — адрес свечей инструмента в ISS.
func (s *priceSource) candlesPath(ctx context.Context, ticker string) (string, error) {
	if ticker == "IMOEX" {
		return "/engines/stock/markets/index/securities/IMOEX", nil
	}
	if m := reOFZNumber.FindStringSubmatch(ticker); m != nil {
		secid, err := s.findBond(ctx, m[1])
		if err != nil {
			return "", err
		}
		return "/engines/stock/markets/bonds/boards/TQOB/securities/" + secid, nil
	}
	tqbr, forts := s.markets(ctx)
	if prefix, ok := futuresPrefix[ticker]; ok {
		if code := frontFutures(prefix, forts); code != "" {
			return "/engines/futures/markets/forts/securities/" + code, nil
		}
		return "", errNoInstrument
	}
	if _, ok := tqbr[ticker]; ok {
		return "/engines/stock/markets/shares/boards/TQBR/securities/" + ticker, nil
	}
	if _, ok := forts[ticker]; ok {
		return "/engines/futures/markets/forts/securities/" + ticker, nil
	}
	if len(tqbr) == 0 && len(forts) == 0 {
		return "", errors.New("список инструментов биржи ещё не загружен")
	}
	return "", errNoInstrument
}

type issTable struct {
	Columns []string `json:"columns"`
	Data    [][]any  `json:"data"`
}

func (s *priceSource) get(ctx context.Context, path string, q url.Values, table string) (issTable, error) {
	q.Set("iss.meta", "off")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+path+"?"+q.Encode(), nil)
	if err != nil {
		return issTable{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return issTable{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return issTable{}, fmt.Errorf("iss %s: HTTP %d", path, resp.StatusCode)
	}
	var body map[string]issTable
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return issTable{}, fmt.Errorf("iss %s: %w", path, err)
	}
	return body[table], nil
}

// findBond — код ОФЗ по номеру выпуска: «ОФЗ 26248» → SU26248RMFS3 (TQOB).
func (s *priceSource) findBond(ctx context.Context, number string) (string, error) {
	t, err := s.get(ctx, "/securities.json", url.Values{
		"q": {number}, "iss.only": {"securities"}, "securities.columns": {"secid,primary_boardid"},
	}, "securities")
	if err != nil {
		return "", err
	}
	for _, row := range t.Data {
		if len(row) >= 2 {
			secid, _ := row[0].(string)
			board, _ := row[1].(string)
			if board == "TQOB" && strings.Contains(secid, number) {
				return secid, nil
			}
		}
	}
	return "", errNoInstrument
}

// lastCloseBefore — закрытие последней свечи, начавшейся не позже момента at.
func lastCloseBefore(t issTable, at time.Time) (float64, bool) {
	ci, bi := -1, -1
	for i, c := range t.Columns {
		switch c {
		case "close":
			ci = i
		case "begin":
			bi = i
		}
	}
	if ci < 0 || bi < 0 {
		return 0, false
	}
	var best float64
	found := false
	for _, row := range t.Data {
		if len(row) <= ci || len(row) <= bi {
			continue
		}
		b, _ := row[bi].(string)
		begin, err := time.ParseInLocation("2006-01-02 15:04:05", b, msk)
		if err != nil || begin.After(at) {
			continue
		}
		if v, ok := row[ci].(float64); ok && v > 0 {
			best, found = v, true
		}
	}
	return best, found
}

// priceAt — цена инструмента на момент at: минутная свеча, в которую попал
// момент (или последняя до него). Сообщение ночью или в выходные — закрытие
// последнего часа торгов перед ним.
func (s *priceSource) priceAt(ctx context.Context, ticker string, at time.Time) (float64, error) {
	path, err := s.candlesPath(ctx, ticker)
	if err != nil {
		return 0, err
	}
	t := at.In(msk).Truncate(time.Minute)
	const layout = "2006-01-02 15:04"
	tries := []struct {
		interval string
		back     time.Duration
	}{{"1", 2 * time.Hour}, {"60", 5 * 24 * time.Hour}}
	for _, tr := range tries {
		tbl, err := s.get(ctx, path+"/candles.json", url.Values{
			"interval": {tr.interval}, "from": {t.Add(-tr.back).Format(layout)}, "till": {t.Format(layout)},
			"candles.columns": {"close,begin"},
		}, "candles")
		if err != nil {
			return 0, err
		}
		if p, ok := lastCloseBefore(tbl, t); ok {
			return p, nil
		}
	}
	return 0, fmt.Errorf("нет сделок по %s до %s", ticker, t.Format(layout))
}

// RunTradePriceFill раз в минуту подгружает цены из очереди trade_price_fills.
func RunTradePriceFill(ctx context.Context, store *storage.Store, log zerolog.Logger) {
	runTradePriceFill(ctx, store, newPriceSource(), log)
}

func runTradePriceFill(ctx context.Context, store *storage.Store, src *priceSource, log zerolog.Logger) {
	tick := time.NewTicker(priceFillEvery)
	defer tick.Stop()
	for {
		fillDuePrices(ctx, store, src, log)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func fillDuePrices(ctx context.Context, store *storage.Store, src *priceSource, log zerolog.Logger) {
	due, err := store.DuePriceFills(ctx, time.Now().Add(-issDelay), 20)
	if err != nil {
		log.Warn().Err(err).Msg("сделки: очередь подгрузки цен не читается")
		return
	}
	for _, f := range due {
		reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		price, err := src.priceAt(reqCtx, f.Ticker, f.At)
		cancel()
		if err == nil {
			if err := store.CompletePriceFill(ctx, f, price); err != nil {
				log.Warn().Err(err).Int64("position", f.PositionID).Msg("сделки: цена не записалась")
				continue
			}
			log.Info().Int64("position", f.PositionID).Str("ticker", f.Ticker).Str("field", f.Field).
				Float64("price", price).Time("at", f.At).Msg("сделки: цена подгружена с биржи")
			continue
		}
		final := errors.Is(err, errNoInstrument) || f.Attempts+1 >= priceFillMaxTry
		next := time.Now().Add(time.Duration(f.Attempts+1) * 2 * time.Minute)
		if rerr := store.RetryPriceFill(ctx, f.ID, err.Error(), next, final); rerr != nil {
			log.Warn().Err(rerr).Msg("сделки: не удалось отложить подгрузку")
		}
		if final {
			log.Info().Err(err).Int64("position", f.PositionID).Str("ticker", f.Ticker).
				Msg("сделки: цену подгрузить нельзя")
		}
	}
}
