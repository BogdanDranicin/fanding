package robots

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Акции ходят через основной режим торгов TQBR, фьючерсы адресуются на уровне рынка.
const (
	stockEngine = "stock"
	stockMarket = "shares"
	stockBoard  = "TQBR"

	futEngine = "futures"
	futMarket = "forts"
)

// MarketTape — лента целого рынка. ISS отдаёт сделки всех инструментов рынка одним
// запросом, с бумагой в колонке SECID, и это единственный доступный способ следить
// за сотнями тикеров: поштучный опрос требовал бы отдельного запроса на каждый
// тикер за интервал — на всех акциях это под сотню запросов в секунду.
type MarketTape struct {
	// Name — как лента называется на странице и в логах.
	Name   string
	Engine string
	Market string
	Board  string
}

// DefaultTapes — ленты, которые опрашивает сбор: весь основной режим акций и весь
// срочный рынок. Что из них берётся в работу, решает Watchlist.
func DefaultTapes() []MarketTape {
	return []MarketTape{
		{Name: "акции TQBR", Engine: stockEngine, Market: stockMarket, Board: stockBoard},
		{Name: "срочный рынок FORTS", Engine: futEngine, Market: futMarket},
	}
}

// indexFutureRoots — фьючерсы на индексы, которые нужны в поиске роботов.
var indexFutureRoots = []string{"MIX", "MXI", "IMOEXF", "RTS", "RGBI"}

// perpetualRe — вечный контракт FORTS: тикер из одних букв, оканчивающийся на F
// (SBERF, GAZPF, USDRUBF, IMOEXF). Квартальные несут дефис и код экспирации
// (Si-9.26), поэтому под правило не попадают.
var perpetualRe = regexp.MustCompile(`^[A-Z]+F$`)

// Watchlist решает, какие инструменты из лент рынков брать в работу.
//
// По умолчанию это все акции основного режима плюс со срочного рынка — валютные
// фьючерсы, индексные фьючерсы и вечные контракты на акции. Срочный рынок целиком
// не берём: там под пять сотен контрактов, и большинство из них неликвидны.
type Watchlist struct {
	// only — явный список тикеров из ROBOTS_SYMBOLS; nil, если работают правила.
	only map[string]bool
}

// NewWatchlist собирает список наблюдения. Пустая спецификация означает правила
// по умолчанию.
func NewWatchlist(spec string) (*Watchlist, error) {
	w := &Watchlist{}
	if strings.TrimSpace(spec) == "" {
		return w, nil
	}
	only := map[string]bool{}
	for _, raw := range strings.Split(spec, ",") {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		// Префикс futures: остался от поштучного опроса и больше ничего не значит:
		// рынок инструмента определяется тем, в чьей ленте он пришёл. Принимаем его
		// молча, чтобы старые конфигурации не падали после обновления.
		if i := strings.IndexByte(item, ':'); i >= 0 {
			if !strings.EqualFold(item[:i], "futures") {
				return nil, fmt.Errorf("robots: неизвестный префикс в записи %q (поддерживается только futures:)", item)
			}
			item = strings.TrimSpace(item[i+1:])
		}
		if item == "" {
			return nil, fmt.Errorf("robots: пустой тикер в списке %q", spec)
		}
		only[strings.ToUpper(item)] = true
	}
	if len(only) == 0 {
		return nil, fmt.Errorf("robots: список тикеров пуст")
	}
	w.only = only
	return w, nil
}

// Keep — брать ли инструмент в работу.
func (w *Watchlist) Keep(secid string, tape MarketTape) bool {
	if secid == "" {
		return false
	}
	sym := strings.ToUpper(secid)
	if w.only != nil {
		return w.only[sym]
	}
	// Акции основного режима берём все: лента уже сужена бордом TQBR.
	if tape.Engine == stockEngine {
		return true
	}
	return IsCurrencyTicker(sym) || isIndexFuture(sym) || perpetualRe.MatchString(sym)
}

// Describe — чем сейчас ограничен сбор; показывается на странице, чтобы пустой
// список роботов не путали с неработающим сбором.
func (w *Watchlist) Describe() string {
	if w.only == nil {
		return "все акции TQBR, валютные и индексные фьючерсы, вечные контракты"
	}
	out := make([]string, 0, len(w.only))
	for s := range w.only {
		out = append(out, s)
	}
	sort.Strings(out)
	return "только " + strings.Join(out, ", ")
}

func isIndexFuture(sym string) bool {
	for _, root := range indexFutureRoots {
		if sym == root || strings.HasPrefix(sym, root+"-") {
			return true
		}
	}
	return false
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

// IsCurrencyTicker — валютный ли это инструмент. Влияет на порог обнаружения:
// у валюты робот заявляется со второго повторяющегося принта, а не с третьего.
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
