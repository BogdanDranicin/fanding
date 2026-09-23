// Package trades разбирает сообщения авторских каналов о сделках и ведёт по ним
// позиции: кто, что, в какую сторону, каким размером.
//
// Разбор — правила по ключевым словам, без ИИ. Авторы пишут свободным текстом
// («6500 роснефти взял 262,95», «Модельный портфель 1 закрыл Лукойл 50%»,
// «#GMKN Открыл шорт по ГМК 128,28, стоп 130,85»), поэтому правила ловят
// типовые обороты, а всё, что они поняли не так, правится на сайте руками.
package trades

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Action string

const (
	ActOpen     Action = "open"
	ActAdd      Action = "add"
	ActReduce   Action = "reduce"
	ActClose    Action = "close"
	ActCloseAll Action = "close_all"
	ActStop     Action = "stop"
)

type Direction string

const (
	DirLong  Direction = "long"
	DirShort Direction = "short"
	DirNone  Direction = ""
)

// UnknownTicker — сделка без названного инструмента («попробовал шорт открыть»).
// Позиция всё равно заводится, чтобы её было видно и можно было поправить руками.
const UnknownTicker = "?"

// Signal — одно действие, понятое из сообщения.
type Signal struct {
	Action    Action
	Ticker    string
	Direction Direction
	Size      string
	Price     *float64
	Stop      string
	Targets   string
	Book      string
	Clause    string
}

type clause struct {
	text     string
	action   Action
	dir      Direction
	tickers  []string
	size     string
	sizeEach bool
	price    *float64
	stop     string
	targets  string
	book     string
	words    int
	hasVerb  bool
	inferred bool
	header   bool
	bare     bool
	inherits bool
}

var (
	reSentence  = regexp.MustCompile(`[.!?…]+(?:\s+|$)|;`)
	reWord      = regexp.MustCompile(`[#$]?[\p{L}\p{N}][\p{L}\p{N}\-+]*`)
	reNumber    = regexp.MustCompile(`\d{1,3}(?: \d{3})+(?:[.,]\d+)?|\d+(?:[.,]\d+)?`)
	reWasParen  = regexp.MustCompile(`\(\s*было[^)]*\)`)
	reBook      = regexp.MustCompile(`п[оа]р\p{L}{3,7}\s+([12])(?:\D|$)`)
	rePnl       = regexp.MustCompile(`[+-]\s*\d+(?:[.,]\d+)?\s*(?:%|к(?:[^\p{L}]|$)|k(?:[^\p{L}]|$)|р(?:[^\p{L}]|$))?`)
	reTimes     = regexp.MustCompile(`в\s+(\d+)\s+раз\p{L}*`)
	reStop      = regexp.MustCompile(`(?:перен\p{L}*\s+)?стоп\p{L}*\s+(?:перен\p{L}*\s+)?((?:в\s+бу|в\s+безуб|под|над|выше|ниже|на|за|\d)(?:[^,;]|,\d)*)`)
	reTargets   = regexp.MustCompile(`(профиты|профит|тейки|тейк|цели|цель|тп)\s*:?\s*(\d[\d\s.,и/]*)`)
	reAvg       = regexp.MustCompile(`средн\p{L}*\s+(\d+(?:[.,]\d+)?)`)
	rePercent   = regexp.MustCompile(`(до\s+(?:\p{L}+\s+){0,4})?(\d+(?:[.,]\d+)?)\s*%`)
	reQtyUnit   = regexp.MustCompile(`(\d{1,3}(?: \d{3})+|\d+)\s*(лот\p{L}*|шт\p{L}*|акци\p{L}*|контракт\p{L}*)`)
	reUpTo      = regexp.MustCompile(`до\s+(\d+)\s*(к|k)?(?:[^\p{L}]|$)`)
	reQtyDir    = regexp.MustCompile(`(?:^|[^\d.,])(\d{1,3}(?: \d{3})+|\d+)\s+(?:шорт\p{L}*|лонг\p{L}*)`)
	reQtyAfter  = regexp.MustCompile(`^\s+(\d{1,3}(?: \d{3})+|\d+)\s+(?:шорт\p{L}*\s+)?(?:взял|верн|добав|шорт)`)
	reHold      = regexp.MustCompile(`^[^,.;]*(?:держу|подержу|оставляю|оставил|оставила|тяну)`)
	reFutCode   = regexp.MustCompile(`^[A-Z][A-Za-z]{1,3}[FGHJKMNQUVXZ]\d$`)
	reQtyBefore = regexp.MustCompile(`(\d{1,3}(?: \d{3})+|\d+)\s+(?:(?:шорт|лонг)\p{L}*\s+)?$`)
	reOFZ       = regexp.MustCompile(`офз\s*(\d{5})?`)
	rePerEach   = regexp.MustCompile(`(?:^|[^\p{L}])по\s+\d+(?:[.,]\d+)?\s*%$`)
	reBareNum   = regexp.MustCompile(`^\d+(?:[.,]\d+)?$`)
)

func words(list ...string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[^\p{L}])(` + strings.Join(list, "|") + `)(?:[^\p{L}]|$)`)
}

const (
	closeVerbs = `(?:закрыл\p{L}*|закрываю|прикрыл\p{L}*|скинул\p{L}*|закрыть|скинуть)`
	closePast  = `(?:закрыл\p{L}*|прикрыл\p{L}*|скинул\p{L}*)`
	dirWords   = `(шорт\p{L}*|лонг\p{L}*|позиц\p{L}*)`
)

var (
	reCloseAll = regexp.MustCompile(closeVerbs + `\s+(?:\p{L}+\s+){0,2}(?:вс[её]|текущие)(?:\s+` + dirWords + `|\s*(?:[^\p{L}\s]|$))` +
		`|` + closePast + `\s+(шорты|лонги)(?:[^\p{L}]|$)` +
		`|вс[её]\s+` + dirWords + `\s+` + closeVerbs +
		`|(?:^|[^\p{L}])(шорты|лонги)\s+` + closePast +
		`|(?:^|[^\p{L}])вс[её]\s+` + closePast +
		`|без\s+позиций`)
	reCloseHdr = regexp.MustCompile(`закрытые\s+позиц`)
	reDecided  = regexp.MustCompile(`решил\p{L}*[^,.]*\s(скинуть|закрыть|продать|выйти)`)
	reClose    = words("закрыл", "закрыла", "закрываю", "скинул", "скинула", "откупил", "откупила", "продал", "продала", "зафиксировал", "зафиксировала", "вышел\\s+из", "вышла\\s+из")
	reReduce   = words("сократил", "сократила", "порезал", "порезала", "срезал", "срезала", "прикрыл", "прикрыла", "урезал", "отдал", "отдала")
	reAdd      = words("добавил", "добавила", "добавился", "добрал", "добрала", "докупил", "докупила", "увеличил", "увеличила", "нарастил", "усреднил", "усреднился", "долил", "вернул", "вернула", "довел", "довела", "восстановил", "восстановила")
	reOpen     = words("купил", "купила", "покупка", "покупаю", "взял", "взяла", "беру", "открыл", "открыла", "открываю", "набрал", "набрала", "зашел", "зашла", "вошел", "вошла", "шортанул", "шортанула", "зашортил", "вшортил", "пошортил", "лонганул", "лонганула")
	reBuy      = words("купил", "купила", "покупка", "покупаю")
	reBuyNoun  = words("покупка")
	reAttempt  = regexp.MustCompile(`(?:пробую|попробовал\p{L}*)\s+(?:\p{L}+\s+){0,2}(?:пошортить|шорт\p{L}*|лонг\p{L}*|купить)`)
	reShort    = words("шорт", "шорта", "шорту", "шортом", "шорты", "шортов", "шортанул", "шортанула", "зашортил", "вшортил", "пошортил", "пошортить", "шортить", "шорчу", "шортецкого")
	reLong     = words("лонг", "лонга", "лонги", "лонгов", "лонгом", "лонганул", "лонганула")
	rePart     = words("еще", "половину", "половина", "часть", "частично")
	reRest     = words("остаток", "остатки", "остальное", "полностью", "весь", "всю", "целиком")
	reModal    = words("не", "бы", "если", "жду", "ждем", "готов", "готова", "хочу", "хочется", "планирую", "думаю", "может", "можно", "стоит", "надо", "нужно", "посмотрю", "буду", "будем", "рекомендую", "что", "когда", "почему", "зачем", "кто", "лучше", "возможно", "знаете", "зря")
	reProfit   = regexp.MustCompile(`прибыл`)
	reNews     = regexp.MustCompile(`#новост|актуальные новости|почему разбираю|статус моих позиций|по мнению|обзор от|по оценке|новости к утру|события (?:на неделю|понедельника|вторника|среды|четверга|пятницы)`)
)

type tickerStem struct {
	stem   string
	ticker string
	strict bool
}

var tickerStems = []tickerStem{
	{"сбербанк-п", "SBERP", false}, {"сберп", "SBERP", false}, {"сбербанк", "SBER", false}, {"сбер", "SBER", true},
	{"газпромнефт", "SIBN", false}, {"газпром", "GAZP", false}, {"газик", "GAZP", true},
	{"лукойл", "LKOH", false}, {"роснефт", "ROSN", false}, {"ростнефт", "ROSN", false}, {"росн", "ROSN", true},
	{"новатэк", "NVTK", false}, {"новатек", "NVTK", false}, {"втб", "VTBR", true},
	{"микс", "MIX", true}, {"mix", "MIX", true}, {"ртс", "RTS", true},
	{"самолет", "SMLT", false}, {"самик", "SMLT", true}, {"магнит", "MGNT", true},
	{"нлмк", "NLMK", true}, {"ммк", "MAGN", true}, {"северстал", "CHMF", false},
	{"полюс", "PLZL", true}, {"норникел", "GMKN", false}, {"норник", "GMKN", true}, {"норк", "GMKN", true}, {"гмк", "GMKN", true},
	{"яндекс", "YDEX", false}, {"озон", "OZON", true}, {"т-технолог", "T", false}, {"тинькоф", "T", false},
	{"мтс", "MTSS", true}, {"мосбирж", "MOEX", false}, {"алрос", "ALRS", false},
	{"татнефт", "TATN", false}, {"сургут", "SNGS", false}, {"русал", "RUAL", true},
	{"аэрофлот", "AFLT", false}, {"аэрик", "AFLT", true}, {"юнипро", "UPRO", false},
	{"интеррао", "IRAO", false}, {"фосагр", "PHOR", false}, {"пятерочк", "X5", false},
	{"хэдхантер", "HEAD", false}, {"астра", "ASTR", true}, {"совкомфлот", "FLOT", false},
	{"совкомбанк", "SVCB", false}, {"селигдар", "SELG", false}, {"селег", "SELG", false}, {"спб", "SPBE", true},
	{"мечел", "MTLR", false}, {"русгидро", "HYDR", false}, {"россет", "FEES", false},
	{"транснефт", "TRNFP", false}, {"ростелеком", "RTKM", false}, {"башнефт", "BANE", false},
	{"вуш", "WUSH", true}, {"вк", "VKCO", true}, {"мвидео", "MVID", false}, {"эталон", "ETLN", true},
	{"сегеж", "SGZH", true}, {"камаз", "KMAZ", true}, {"белуг", "BELU", true},
	{"европлан", "LEAS", false}, {"распадск", "RASP", false}, {"софтлайн", "SOFL", false},
	{"сишк", "Si", false}, {"брент", "BR", true}, {"нефть", "BR", true}, {"нефти", "BR", true},
	{"золот", "GOLD", false}, {"серебр", "SILV", false}, {"платин", "PLT", false},
	{"биткоин", "BTC", false}, {"биткойн", "BTC", false}, {"биток", "BTC", true}, {"эфир", "ETH", true},
	{"индекс", "IMOEX", true}, {"рынок", "IMOEX", true}, {"имоекс", "IMOEX", true},
}

var knownLatin = func() map[string]bool {
	m := map[string]bool{}
	for _, s := range tickerStems {
		m[strings.ToUpper(s.ticker)] = true
	}
	for _, t := range []string{"SBER", "GAZP", "LKOH", "ROSN", "NVTK", "VTBR", "MIX", "MXI", "RTS", "RI", "SMLT",
		"MGNT", "NLMK", "MAGN", "CHMF", "PLZL", "GMKN", "YDEX", "OZON", "MTSS", "MOEX", "ALRS", "TATN", "TATNP",
		"SNGS", "SNGSP", "RUAL", "AFLT", "UPRO", "IRAO", "PHOR", "X5", "HEAD", "ASTR", "POSI", "FLOT", "SVCB",
		"SELG", "SPBE", "MTLR", "HYDR", "FEES", "TRNFP", "RTKM", "BANE", "WUSH", "MVID", "VKCO", "SIBN", "SBERP",
		"IMOEX", "BTC", "ETH", "GOLD", "SILV", "BR", "SI", "CNY", "PIKK", "AFKS", "CBOM", "ENPG", "UGLD", "LENT"} {
		m[t] = true
	}
	return m
}()

// Parse разбирает одно сообщение. replyTickers — тикеры сообщения, на которое
// это отвечает: «Закрыл» ответом на пост о покупке относится к той же бумаге.
// maxTradeRunes — длиннее этого сообщения о сделках не бывают: это разборы и
// обзоры («Совет трейдеров» на пару экранов), где «покупаю» и «продажа» —
// сценарии, а не действия автора.
const maxTradeRunes = 1500

func Parse(text string, replyTickers []string) []Signal {
	t := normalize(text)
	if utf8.RuneCountInString(t) > maxTradeRunes || reNews.MatchString(strings.ToLower(t)) {
		return nil
	}

	var cls []*clause
	for _, line := range strings.Split(t, "\n") {
		for _, part := range splitSentences(line) {
			if c := analyze(part); c != nil {
				cls = append(cls, c)
			}
		}
	}

	inheritActions(cls)

	used := map[string]bool{}
	for _, c := range cls {
		if c.hasVerb && !c.inherits {
			for _, tk := range c.tickers {
				used[tk] = true
			}
		}
	}
	var fallback []string
	for _, tk := range tagTickers(t) {
		if !used[tk] {
			fallback = append(fallback, tk)
		}
	}

	var out []Signal
	seen := map[string]int{}
	for _, c := range cls {
		if c.action == "" || c.header {
			continue
		}
		for _, s := range signalsOf(c, fallback, replyTickers) {
			key := string(s.Action) + "|" + s.Ticker
			if i, ok := seen[key]; ok {
				out[i] = merge(out[i], s)
				continue
			}
			seen[key] = len(out)
			out = append(out, s)
		}
	}
	return out
}

func merge(a, b Signal) Signal {
	if a.Direction == DirNone {
		a.Direction = b.Direction
	}
	if a.Size == "" {
		a.Size = b.Size
	}
	if a.Price == nil {
		a.Price = b.Price
	}
	if a.Stop == "" {
		a.Stop = b.Stop
	}
	if a.Targets == "" {
		a.Targets = b.Targets
	}
	if a.Book == "" {
		a.Book = b.Book
	}
	if len(b.Clause) > len(a.Clause) {
		a.Clause = b.Clause
	}
	return a
}

// MessageTickers — все инструменты, упомянутые в сообщении, по порядку. По
// ним ответ «Закрыл» на это сообщение узнаёт, о какой бумаге речь.
func MessageTickers(text string) []string {
	t := normalize(text)
	lower := strings.ToLower(t)
	var out []string
	for _, m := range reOFZ.FindAllStringSubmatchIndex(lower, -1) {
		if boundaryAt(lower, m[0]) {
			out = appendUnique(out, ofzTicker(lower, m))
		}
	}
	for _, f := range findTickers(t, lower) {
		out = appendUnique(out, f.ticker)
	}
	return out
}

// tagTickers — тикеры, отмеченные #/$. Только ими можно дополнить фразу без
// тикера («#OZON … Закрыл остаток»): упоминание бумаги в соседнем рассуждении
// («там нефть пошла») сделкой по ней не является.
func tagTickers(t string) []string {
	var out []string
	for _, f := range findTickers(t, strings.ToLower(t)) {
		if f.tag {
			out = appendUnique(out, f.ticker)
		}
	}
	return out
}

var homoglyphs = map[rune]rune{
	'a': 'а', 'e': 'е', 'o': 'о', 'p': 'р', 'c': 'с', 'x': 'х', 'y': 'у', 'k': 'к', 'm': 'м', 't': 'т', 'h': 'н', 'b': 'в',
	'A': 'А', 'E': 'Е', 'O': 'О', 'P': 'Р', 'C': 'С', 'X': 'Х', 'Y': 'У', 'K': 'К', 'M': 'М', 'T': 'Т', 'H': 'Н', 'B': 'В',
}

var reMixedWord = regexp.MustCompile(`[\p{L}]+`)

// normalize сводит текст к одному виду. Отдельно — латинские буквы внутри
// русских слов («самолeт» с латинской e): так пишут, чтобы обойти поиск, и
// без замены такое слово не узнаётся.
func normalize(s string) string {
	r := strings.NewReplacer("ё", "е", "Ё", "Е", " ", " ", "−", "-", "–", "-", "—", "-", "\r", "")
	s = r.Replace(s)
	return reMixedWord.ReplaceAllStringFunc(s, func(w string) string {
		hasCyr, hasLat := false, false
		for _, ch := range w {
			switch {
			case unicode.Is(unicode.Cyrillic, ch):
				hasCyr = true
			case ch < 128:
				hasLat = true
			}
		}
		if !hasCyr || !hasLat {
			return w
		}
		return strings.Map(func(ch rune) rune {
			if c, ok := homoglyphs[ch]; ok {
				return c
			}
			return ch
		}, w)
	})
}

func splitSentences(line string) []string {
	var out []string
	last := 0
	for _, m := range reSentence.FindAllStringIndex(line, -1) {
		out = append(out, line[last:m[1]])
		last = m[1]
	}
	out = append(out, line[last:])
	var res []string
	for _, p := range out {
		if p = strings.TrimSpace(p); p != "" {
			res = append(res, p)
		}
	}
	return res
}

type found struct {
	ticker     string
	start, end int
	tag        bool
}

func boundaryAt(s string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

func ofzTicker(lower string, m []int) string {
	if m[2] >= 0 {
		return "ОФЗ " + lower[m[2]:m[3]]
	}
	return "ОФЗ"
}

func findTickers(orig, lower string) []found {
	var out []found
	for _, loc := range reWord.FindAllStringIndex(lower, -1) {
		if !boundaryAt(lower, loc[0]) {
			continue
		}
		w := lower[loc[0]:loc[1]]
		ow := orig[loc[0]:loc[1]]
		if tk := wordTicker(w, ow); tk != "" {
			out = append(out, found{tk, loc[0], loc[1], strings.HasPrefix(w, "#") || strings.HasPrefix(w, "$")})
		}
	}
	return out
}

func wordTicker(w, orig string) string {
	if strings.HasPrefix(w, "#") || strings.HasPrefix(w, "$") {
		tag := strings.ToUpper(w[1:])
		if isLatinTicker(tag) {
			if tag == "SI" {
				return "Si"
			}
			return tag
		}
		return ""
	}
	if orig == "Т" {
		return "T"
	}
	if isLatinTicker(orig) && (knownLatin[orig] || reFutCode.MatchString(orig)) {
		if orig == "SI" {
			return "Si"
		}
		return orig
	}
	for _, s := range tickerStems {
		if !strings.HasPrefix(w, s.stem) {
			continue
		}
		extra := utf8.RuneCountInString(w) - utf8.RuneCountInString(s.stem)
		if s.strict && extra > 2 {
			continue
		}
		return s.ticker
	}
	return ""
}

func isLatinTicker(s string) bool {
	if s == "" || len(s) > 8 {
		return false
	}
	letters := 0
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			letters++
		case r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return letters > 0
}

func maskRange(s string, a, b int) string {
	return s[:a] + strings.Repeat(" ", b-a) + s[b:]
}

func mostlyUpper(s string) bool {
	up, low := 0, 0
	for _, r := range s {
		if unicode.IsUpper(r) {
			up++
		} else if unicode.IsLower(r) {
			low++
		}
	}
	return up+low > 25 && up > 3*low
}

func runeGap(s string, a, b int) int {
	if b <= a {
		return 0
	}
	return utf8.RuneCountInString(s[a:b])
}

// analyze разбирает одну фразу. Всё, что опознано (портфель, результат сделки,
// стоп, цели, тикеры, размер), вырезается из строки пробелами той же длины —
// так смещения в исходной и строчной копиях совпадают, а цена в конце ищется
// среди оставшихся чисел и не путается со стопом или размером.
func analyze(text string) *clause {
	trimmed := strings.TrimSpace(text)
	if mostlyUpper(trimmed) || strings.HasSuffix(trimmed, "?") {
		return nil
	}
	orig := reWasParen.ReplaceAllStringFunc(text, func(m string) string { return strings.Repeat(" ", len(m)) })
	lower := strings.ToLower(orig)
	if len(lower) != len(orig) {
		lower = orig
	}
	c := &clause{text: trimmed}
	c.header = strings.HasSuffix(strings.TrimRight(trimmed, " "), ":")
	for _, w := range reWord.FindAllString(lower, -1) {
		if !reBareNum.MatchString(w) {
			c.words++
		}
	}

	mask := func(a, b int) {
		lower = maskRange(lower, a, b)
		orig = maskRange(orig, a, b)
	}

	if m := reBook.FindStringSubmatchIndex(lower); m != nil {
		c.book = "МП" + lower[m[2]:m[3]]
		mask(m[0], m[3])
	}

	var ofz []found
	for _, m := range reOFZ.FindAllStringSubmatchIndex(lower, -1) {
		if !boundaryAt(lower, m[0]) {
			continue
		}
		ofz = append(ofz, found{ofzTicker(lower, m), m[0], m[1], false})
		mask(m[0], m[1])
	}

	pnl := false
	for _, m := range rePnl.FindAllStringIndex(lower, -1) {
		if m[0] > 0 {
			r, _ := utf8.DecodeLastRuneInString(lower[:m[0]])
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				continue
			}
		}
		pnl = true
		mask(m[0], m[1])
	}
	won := strings.Contains(text, "✅")
	lost := strings.Contains(text, "❌")

	if m := reTimes.FindStringSubmatchIndex(lower); m != nil {
		c.size = "×" + lower[m[2]:m[3]]
		mask(m[0], m[1])
	}

	var stopNum *float64
	if m := reStop.FindStringSubmatchIndex(lower); m != nil {
		c.stop = strings.TrimSpace(strings.TrimRight(orig[m[2]:m[3]], " ,.;:"))
		if utf8.RuneCountInString(c.stop) > 60 {
			c.stop = string([]rune(c.stop)[:60])
		}
		if i := strings.IndexAny(c.stop, "(✅❌"); i >= 0 {
			c.stop = strings.TrimSpace(c.stop[:i])
		}
		if p, ok := parseNumber(firstNumber(c.stop)); ok && strings.IndexFunc(c.stop, unicode.IsDigit) == 0 {
			stopNum = &p
		}
		mask(m[0], m[1])
	}

	anyVerb := reOpen.MatchString(lower) || reAttempt.MatchString(lower) || reClose.MatchString(lower) ||
		reReduce.MatchString(lower) || reAdd.MatchString(lower) || reCloseAll.MatchString(lower)
	singleProfit := false
	if m := reTargets.FindStringSubmatchIndex(lower); m != nil {
		word := lower[m[2]:m[3]]
		nums := strings.TrimSpace(strings.TrimRight(orig[m[4]:m[5]], " ,.;"))
		if word == "профит" && pnl && !anyVerb {
			singleProfit = true
			if p, ok := parseNumber(firstNumber(nums)); ok {
				c.price = &p
			}
		} else {
			c.targets = nums
		}
		mask(m[0], m[1])
	}

	if m := reAvg.FindStringSubmatchIndex(lower); m != nil {
		if p, ok := parseNumber(lower[m[2]:m[3]]); ok {
			c.price = &p
		}
		mask(m[0], m[1])
	}

	verbPos, verbEnd := -1, -1
	decided := false
	buyNoun := false
	setVerb := func(a Action, pos, end int) {
		if c.action == "" {
			c.action = a
			verbPos, verbEnd = pos, end
		}
	}
	if m := reCloseAll.FindStringSubmatchIndex(lower); m != nil {
		setVerb(ActCloseAll, m[0], m[1])
		for g := 2; g+1 < len(m); g += 2 {
			if m[g] < 0 {
				continue
			}
			switch w := lower[m[g]:m[g+1]]; {
			case strings.HasPrefix(w, "шорт"):
				c.dir = DirShort
			case strings.HasPrefix(w, "лонг"):
				c.dir = DirLong
			}
		}
	}
	if m := reCloseHdr.FindStringIndex(lower); m != nil {
		setVerb(ActClose, m[0], m[1])
	}
	if m := reDecided.FindStringIndex(lower); m != nil {
		setVerb(ActClose, m[0], m[1])
		decided = c.action == ActClose
	}
	if m := reClose.FindStringSubmatchIndex(lower); m != nil {
		verb := lower[m[2]:m[3]]
		if strings.HasPrefix(verb, "продал") && strings.Contains(lower, "в шорт") {
			setVerb(ActOpen, m[2], m[3])
			c.dir = DirShort
		} else {
			setVerb(ActClose, m[2], m[3])
			if strings.HasPrefix(verb, "откупил") && c.action == ActClose {
				c.dir = DirShort
			}
		}
	}
	if m := reReduce.FindStringSubmatchIndex(lower); m != nil {
		setVerb(ActReduce, m[2], m[3])
	}
	if m := reAdd.FindStringSubmatchIndex(lower); m != nil {
		setVerb(ActAdd, m[2], m[3])
	}
	if m := reOpen.FindStringSubmatchIndex(lower); m != nil {
		setVerb(ActOpen, m[2], m[3])
		buyNoun = c.action == ActOpen && reBuyNoun.MatchString(lower[m[0]:m[1]])
	}
	if m := reAttempt.FindStringIndex(lower); m != nil {
		setVerb(ActOpen, m[0], m[1])
	}
	if singleProfit {
		setVerb(ActReduce, 0, 0)
	}

	if c.action != "" && verbPos >= 0 {
		before := lower[:verbPos]
		if i := strings.LastIndex(before, ","); i >= 0 {
			before = before[i+1:]
		}
		after := strings.TrimSpace(lower[verbEnd:])
		if reModal.MatchString(before) || strings.HasPrefix(after, "бы ") || after == "бы" {
			return nil
		}
	}

	ft := append(ofz, findTickers(orig, lower)...)
	sortFound(ft)
	if len(ft) > 0 && !ft[0].tag {
		if m := reQtyBefore.FindStringSubmatchIndex(lower[:ft[0].start]); m != nil {
			c.size = strings.ReplaceAll(lower[m[2]:m[3]], " ", "") + " шт"
			mask(m[2], m[3])
		}
	}
	if c.size == "" && len(ft) > 0 {
		if m := reQtyAfter.FindStringSubmatchIndex(lower[ft[0].end:]); m != nil {
			a, b := ft[0].end+m[2], ft[0].end+m[3]
			c.size = strings.ReplaceAll(lower[a:b], " ", "") + " шт"
			mask(a, b)
		}
	}
	if c.size == "" {
		if m := reQtyDir.FindStringSubmatchIndex(lower); m != nil {
			c.size = strings.ReplaceAll(lower[m[2]:m[3]], " ", "") + " шт"
			mask(m[2], m[3])
		}
	}
	lastEnd := verbEnd
	scopeEnd := verbEnd
	for _, f := range ft {
		keep := (verbPos < 0 || f.start < verbPos || f.tag || runeGap(lower, lastEnd, f.start) <= 16) &&
			!(verbPos >= 0 && reHold.MatchString(lower[f.end:]))
		if keep {
			c.tickers = appendUnique(c.tickers, f.ticker)
			if f.start >= verbPos {
				lastEnd = f.end
			}
			scopeEnd = max(scopeEnd, f.end)
		}
		mask(f.start, f.end)
	}

	dirScope := lower
	if verbPos >= 0 {
		dirScope = lower[:min(len(lower), scopeEnd+40)]
	}
	if c.dir == DirNone {
		switch {
		case reShort.MatchString(dirScope):
			c.dir = DirShort
		case reLong.MatchString(dirScope):
			c.dir = DirLong
		case c.action == ActOpen && reBuy.MatchString(lower):
			c.dir = DirLong
		}
	}

	if !reProfit.MatchString(lower) {
		first := true
		for _, m := range rePercent.FindAllStringSubmatchIndex(lower, -1) {
			if first {
				v := strings.ReplaceAll(lower[m[4]:m[5]], ",", ".")
				if m[2] >= 0 {
					c.size = "до " + v + "%"
				} else if c.size == "" {
					c.size = v + "%"
				}
				c.sizeEach = rePerEach.MatchString(lower[:m[1]])
				first = false
			}
			mask(m[0], m[1])
		}
	} else {
		for _, m := range rePercent.FindAllStringIndex(lower, -1) {
			mask(m[0], m[1])
		}
	}
	if m := reQtyUnit.FindStringSubmatchIndex(lower); m != nil {
		n := strings.ReplaceAll(lower[m[2]:m[3]], " ", "")
		unit := "шт"
		switch u := lower[m[4]:m[5]]; {
		case strings.HasPrefix(u, "лот"):
			unit = "лот"
		case strings.HasPrefix(u, "контракт"):
			unit = "контр"
		}
		if c.size == "" {
			c.size = n + " " + unit
		}
		mask(m[0], m[1])
	}
	if c.action == ActAdd {
		if m := reUpTo.FindStringSubmatchIndex(lower); m != nil {
			n := lower[m[2]:m[3]]
			if m[4] >= 0 {
				n += "000"
			}
			c.size = "до " + n
			mask(m[0], m[3])
			if m[4] >= 0 {
				mask(m[4], m[5])
			}
		}
	}
	if c.size == "" && strings.Contains(lower, "половин") {
		c.size = "50%"
	}

	rest := reRest.MatchString(lower)
	switch {
	case decided:
		c.size = ""
	case c.action == ActClose && !rest:
		pct, isPct := percentValue(c.size)
		if (isPct && pct < 100) || rePart.MatchString(lower) {
			c.action = ActReduce
		}
	case c.action == ActReduce && rest && c.size == "" && !singleProfit:
		c.action = ActClose
	}

	if c.price == nil {
		for _, loc := range reNumber.FindAllStringIndex(lower, -1) {
			if !boundaryAt(lower, loc[0]) {
				continue
			}
			if p, ok := parseNumber(lower[loc[0]:loc[1]]); ok && p > 0 {
				c.price = &p
				break
			}
		}
	}

	if buyNoun && (len(c.tickers) == 0 || (c.size == "" && c.price == nil)) {
		return nil
	}

	if c.action == "" && len(c.tickers) > 0 {
		switch {
		case c.stop != "" && lost:
			c.action = ActClose
			if c.price == nil {
				c.price = stopNum
			}
		case pnl && won:
			c.action = ActReduce
		case c.dir != DirNone && c.price != nil && c.stop != "":
			c.action = ActOpen
		case c.stop != "":
			c.action = ActStop
		}
		if c.action != "" {
			c.inferred = true
			if reModal.MatchString(lower) {
				return nil
			}
		}
	}
	c.hasVerb = c.action != ""

	if len(c.tickers) > 0 && (!c.hasVerb || c.inferred) {
		left := 0
		for _, w := range reWord.FindAllString(lower, -1) {
			if utf8.RuneCountInString(w) > 2 && !reNumber.MatchString(w) {
				left++
			}
		}
		c.bare = left <= 1
	}
	return c
}

func sortFound(f []found) {
	for i := 1; i < len(f); i++ {
		for j := i; j > 0 && f[j].start < f[j-1].start; j-- {
			f[j], f[j-1] = f[j-1], f[j]
		}
	}
}

func inheritActions(cls []*clause) {
	for i, c := range cls {
		if !c.bare {
			continue
		}
		strong := func(o *clause) bool { return o.hasVerb && (!o.inferred || !c.hasVerb) }
		var src *clause
		for j := i - 1; j >= 0; j-- {
			if strong(cls[j]) && !cls[j].bare {
				src = cls[j]
				break
			}
			if !cls[j].bare {
				break
			}
		}
		if src == nil {
			for j := i + 1; j < len(cls); j++ {
				if strong(cls[j]) && !cls[j].bare {
					src = cls[j]
					break
				}
				if !cls[j].bare {
					break
				}
			}
		}
		if src == nil {
			continue
		}
		switch src.action {
		case ActOpen, ActAdd, ActReduce, ActClose:
			c.action = src.action
			c.hasVerb = true
			c.inherits = true
			if c.dir == DirNone {
				c.dir = src.dir
			}
			if c.book == "" {
				c.book = src.book
			}
		}
	}
}

// shortClause — фраза из пары слов («Закрыл», «Шорт закрыл»). Только такие
// без тикера относим к единственной открытой позиции автора: в длинной фразе
// без тикера («докупку акций решил скинуть») речь может быть о чём угодно.
const shortClause = 2

func signalsOf(c *clause, fallback, replyTickers []string) []Signal {
	base := Signal{
		Action:    c.action,
		Direction: c.dir,
		Size:      c.size,
		Price:     c.price,
		Stop:      c.stop,
		Targets:   c.targets,
		Book:      c.book,
		Clause:    c.text,
	}
	if c.action == ActCloseAll {
		base.Size, base.Price = "", nil
		return []Signal{base}
	}
	tickers := c.tickers
	if len(tickers) == 0 && !c.inherits {
		switch {
		case len(fallback) == 1:
			tickers = fallback
		case len(replyTickers) == 1:
			tickers = replyTickers
		}
	}
	if len(tickers) == 0 {
		switch c.action {
		case ActOpen, ActAdd:
			if c.dir == DirNone {
				return nil
			}
			base.Ticker = UnknownTicker
			return []Signal{base}
		case ActClose:
			if c.words <= shortClause {
				return []Signal{base}
			}
		}
		return nil
	}
	var out []Signal
	for _, tk := range tickers {
		s := base
		s.Ticker = tk
		if len(tickers) > 1 && !c.sizeEach {
			s.Size, s.Price = "", nil
		}
		if len(tickers) > 1 && c.sizeEach {
			s.Price = nil
		}
		out = append(out, s)
	}
	return out
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func firstNumber(s string) string {
	return reNumber.FindString(s)
}

func parseNumber(s string) (float64, bool) {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	s = strings.ReplaceAll(s, ",", ".")
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

func percentValue(size string) (float64, bool) {
	if !strings.HasSuffix(size, "%") || strings.HasPrefix(size, "до ") {
		return 0, false
	}
	v, ok := parseNumber(strings.TrimSuffix(size, "%"))
	return v, ok
}
