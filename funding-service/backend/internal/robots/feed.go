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
	// fortsBoard — как срочный рынок называется в каталоге брокера. В адресе ленты
	// ISS борд не указывается, но инструменты оттуда приходят помеченными им.
	fortsBoard = "SPBFUT"
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

// Срочный рынок адресуется короткими кодами, а не человеческими именами: контракт
// MIX-9.26 приходит в ленте как SECID «MXU6», Si-9.26 — как «SiU6». Поэтому отбор
// идёт по коду базового актива (первые две буквы) плюс месяц и год экспирации.
var fortsQuarterlyRe = regexp.MustCompile(`^([A-Za-z]{2})[FGHJKMNQUVXZ]\d$`)

// perpetualRe — вечный контракт: SECID из одних букв с F на конце (SBERF, GAZPF,
// USDRUBF, IMOEXF). У квартальных в SECID есть цифра года, так что они не пройдут.
var perpetualRe = regexp.MustCompile(`^[A-Z]+F$`)

// Коды базовых активов срочного рынка, которые нас интересуют. Ключи в верхнем
// регистре: биржа пишет коды вперемешку («SiU6», «MXU6»).
var (
	currencyFortsCodes = map[string]bool{
		"SI": true, // доллар к рублю
		"EU": true, // евро к рублю
		"CR": true, // юань к рублю
		"ED": true, // евро к доллару
	}
	indexFortsCodes = map[string]bool{
		"MX": true, // MIX, индекс МосБиржи
		"MM": true, // MXI, тот же индекс мини
		"RI": true, // RTS
	}
)

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
	return IsCurrencyTicker(secid) || isIndexFuture(secid) || perpetualRe.MatchString(sym)
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

// fortsCode возвращает код базового актива квартального контракта в верхнем
// регистре: «SiU6» → «SI». Для вечных и прочих имён — пустая строка.
func fortsCode(secid string) string {
	m := fortsQuarterlyRe.FindStringSubmatch(secid)
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1])
}

// isIndexFuture — фьючерс на индекс: и квартальный MIX/MXI/RTS, и вечный IMOEXF.
func isIndexFuture(secid string) bool {
	if code := fortsCode(secid); code != "" {
		return indexFortsCodes[code]
	}
	return strings.ToUpper(secid) == "IMOEXF"
}

// Вечные контракты несут валютную пару прямо в SECID (USDRUBF, CNYRUBF), поэтому
// их ловим префиксом. Квартальные приходят коротким кодом (SiU6), и для них
// работает разбор через fortsCode.
var (
	// currencyPairPrefixes — тикеры с парой в имени; сверяем префиксом, чтобы
	// покрыть и вечный суффикс F, и прочие хвосты.
	currencyPairPrefixes = []string{
		"USDRUB", "EURRUB", "CNYRUB", "GBPRUB", "CHFRUB", "JPYRUB", "TRYRUB",
		"HKDRUB", "KZTRUB", "BYNRUB", "AMDRUB", "EURUSD", "USDCNY",
	}
)

// IsCurrencyTicker — валютный ли это инструмент. Влияет на порог обнаружения:
// у валюты робот заявляется с третьего повторяющегося принта, а не с шестого.
func IsCurrencyTicker(ticker string) bool {
	t := strings.ToUpper(strings.TrimSpace(ticker))
	// Вечные контракты несут пару прямо в имени: USDRUBF, CNYRUBF.
	for _, p := range currencyPairPrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	// Квартальные — только по коду базового актива. Префиксом сверять нельзя:
	// серебро SILV уехало бы в валюту вслед за долларом Si.
	if code := fortsCode(strings.TrimSpace(ticker)); code != "" {
		return currencyFortsCodes[code]
	}
	return false
}
