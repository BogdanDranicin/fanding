package robots

import (
	"fmt"
	"strings"
)

// Feed — тикер, за лентой сделок которого следим.
type Feed struct {
	// Symbol — как тикер показывается на странице (обычно совпадает с SecID).
	Symbol string
	Engine string
	Market string
	Board  string
	SecID  string
	// Currency — валютный инструмент. Порог обнаружения у валюты ниже (робот
	// заявляется со второго повторяющегося принта, а не с третьего): лента там
	// реже, случайных совпадений размера меньше, и ждать третьего принта — значит
	// увидеть робота на такт позже, чем он начал работать.
	Currency bool
}

// Акции ходят через основной режим торгов TQBR, фьючерсы адресуются на уровне рынка.
const (
	stockEngine = "stock"
	stockMarket = "shares"
	stockBoard  = "TQBR"

	futEngine = "futures"
	futMarket = "forts"
)

// StockFeed — лента акции в основном режиме торгов.
func StockFeed(ticker string) Feed {
	return Feed{Symbol: ticker, Engine: stockEngine, Market: stockMarket, Board: stockBoard, SecID: ticker}
}

// FuturesFeed — лента фьючерса на FORTS.
func FuturesFeed(ticker string) Feed {
	return Feed{Symbol: ticker, Engine: futEngine, Market: futMarket, SecID: ticker, Currency: IsCurrencyTicker(ticker)}
}

// Валютные инструменты MOEX опознаём двумя способами, потому что биржа называет
// их по-разному: вечные контракты несут пару прямо в тикере (USDRUBF), а
// квартальные — короткий код и дату экспирации (Si-9.26).
var (
	// currencyPairPrefixes — тикеры с парой в имени; сверяем префиксом, чтобы
	// покрыть и вечный суффикс F, и прочие хвосты.
	currencyPairPrefixes = []string{
		"USDRUB", "EURRUB", "CNYRUB", "GBPRUB", "CHFRUB", "JPYRUB", "TRYRUB",
		"HKDRUB", "KZTRUB", "BYNRUB", "AMDRUB", "EURUSD", "USDCNY",
	}
	// currencyFortsCodes — короткие коды срочного рынка. Сверяем точно или до
	// дефиса перед экспирацией: префиксом нельзя, иначе серебро SILV уедет
	// в валюту вслед за долларом Si.
	currencyFortsCodes = []string{"SI", "EU", "CR", "ED", "UC", "GBPU", "CHF", "JP", "TR"}
)

// IsCurrencyTicker — валютный ли это инструмент. Влияет только на порог
// обнаружения (см. Feed.Currency).
func IsCurrencyTicker(ticker string) bool {
	t := strings.ToUpper(strings.TrimSpace(ticker))
	for _, p := range currencyPairPrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	for _, code := range currencyFortsCodes {
		if t == code || strings.HasPrefix(t, code+"-") {
			return true
		}
	}
	return false
}

// DefaultFeeds — ликвидные бумаги основного режима плюс валютные фьючерсы, которые
// сервис и так опрашивает ради фандинга. Список переопределяется через ROBOTS_SYMBOLS.
func DefaultFeeds() []Feed {
	tickers := []string{
		"SBER", "GAZP", "LKOH", "VTBR", "ROSN", "GMKN", "TATN", "MTSS",
		"MGNT", "NVTK", "SNGS", "PLZL", "AFLT", "MOEX", "CHMF", "ALRS",
	}
	feeds := make([]Feed, 0, len(tickers)+2)
	for _, t := range tickers {
		feeds = append(feeds, StockFeed(t))
	}
	return append(feeds, FuturesFeed("USDRUBF"), FuturesFeed("CNYRUBF"))
}

// ParseFeeds разбирает список тикеров из конфигурации. По умолчанию запись — акция
// основного режима; префикс "futures:" переводит тикер на срочный рынок.
// Пример: "SBER,GAZP,futures:USDRUBF".
func ParseFeeds(spec string) ([]Feed, error) {
	var feeds []Feed
	seen := map[string]bool{}
	for _, raw := range strings.Split(spec, ",") {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		var f Feed
		switch {
		case strings.HasPrefix(strings.ToLower(item), "futures:"):
			ticker := strings.ToUpper(strings.TrimSpace(item[len("futures:"):]))
			if ticker == "" {
				return nil, fmt.Errorf("robots: пустой тикер в записи %q", item)
			}
			f = FuturesFeed(ticker)
		case strings.Contains(item, ":"):
			return nil, fmt.Errorf("robots: неизвестный префикс в записи %q (поддерживается только futures:)", item)
		default:
			f = StockFeed(strings.ToUpper(item))
		}
		if seen[f.Symbol] {
			continue
		}
		seen[f.Symbol] = true
		feeds = append(feeds, f)
	}
	if len(feeds) == 0 {
		return nil, fmt.Errorf("robots: список тикеров пуст")
	}
	return feeds, nil
}
