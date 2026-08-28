package robots

import (
	"math"
	"sort"
	"time"
)

// Detector хранит скользящее окно ленты по каждому тикеру и ищет в нём роботов.
// Он не ходит в сеть и не знает про базу: принты в него кладёт коллектор.
// Все методы безопасны для последовательного вызова из одной горутины —
// синхронизацию обеспечивает вызывающий (см. Collector).
type Detector struct {
	cfg   Config
	tapes map[string][]Print // symbol -> лента, отсортированная по времени
	// currency — тикеры с пониженным порогом обнаружения (см. Config).
	currency map[string]bool
	maxLen   int
}

// thresholds — условия разбора одной ленты: пороги длины серии и шаг биржевой
// метки времени в ней.
type thresholds struct {
	minPrints int
	minBeats  int
	// grain — с какой точностью источник пишет время сделки. У публичного фида ISS
	// это секунда, у потока брокера — миллисекунды. От него зависит допуск на такт:
	// на секундной метке принт робота приезжает на полсекунды раньше или позже
	// своего места просто из-за округления, и требовать от него точности нельзя.
	grain time.Duration
}

// maxTapeLen — аварийный предел длины ленты, а не рабочий механизм: ленту режет по
// времени Trim, который коллектор зовёт после каждой порции сделок. Предел спасает
// от разрастания памяти, если Trim почему-то не вызывается, но если он всё же
// сработает, из окна анализа пропадёт его начало — поэтому запас выбран щедрый.
const maxTapeLen = 20000

// NewDetector создаёт детектор с заданными порогами.
func NewDetector(cfg Config) *Detector {
	return &Detector{
		cfg:      cfg,
		tapes:    make(map[string][]Print),
		currency: make(map[string]bool),
		maxLen:   maxTapeLen,
	}
}

// MarkCurrency помечает тикеры как валютные: у них порог обнаружения ниже.
func (d *Detector) MarkCurrency(symbols ...string) {
	for _, s := range symbols {
		d.currency[s] = true
	}
}

// thresholdsFor — с какой длины серии заявлять робота по этому инструменту.
func (d *Detector) thresholdsFor(symbol string) thresholds {
	if d.currency[symbol] && d.cfg.MinPrintsCurrency > 0 {
		return thresholds{minPrints: d.cfg.MinPrintsCurrency, minBeats: d.cfg.MinBeatsCurrency}
	}
	return thresholds{minPrints: d.cfg.MinPrints, minBeats: d.cfg.MinBeats}
}

// Add добавляет принты в ленту их тикера. Принты могут приходить пачкой и не
// строго по времени — лента пересортируется по биржевому времени.
func (d *Detector) Add(prints ...Print) {
	touched := make(map[string]bool, 4)
	for _, p := range prints {
		if p.Symbol == "" || p.Qty <= 0 || p.Time.IsZero() {
			continue
		}
		d.tapes[p.Symbol] = append(d.tapes[p.Symbol], p)
		touched[p.Symbol] = true
	}
	for sym := range touched {
		tape := d.tapes[sym]
		sort.Slice(tape, func(i, j int) bool { return tape[i].Time.Before(tape[j].Time) })
		if len(tape) > d.maxLen {
			tape = tape[len(tape)-d.maxLen:]
		}
		d.tapes[sym] = tape
	}
}

// window возвращает часть ленты, попадающую в окно анализа [now-Window, now].
// Верхняя граница нужна не только ради честности среза: без неё скан «задним
// числом» смотрел бы и в будущее относительно своей же точки отсчёта. Принты
// свежее now не выбрасываются, а просто ждут следующего скана.
func (d *Detector) window(tape []Print, now time.Time) []Print {
	cutoff := now.Add(-d.cfg.Window)
	lo := sort.Search(len(tape), func(i int) bool { return !tape[i].Time.Before(cutoff) })
	hi := sort.Search(len(tape), func(i int) bool { return tape[i].Time.After(now) })
	if lo >= hi {
		return nil
	}
	return tape[lo:hi]
}

// Trim выбрасывает из лент всё, что старше окна анализа относительно now.
func (d *Detector) Trim(now time.Time) {
	cutoff := now.Add(-d.cfg.Window)
	for sym, tape := range d.tapes {
		i := sort.Search(len(tape), func(i int) bool { return !tape[i].Time.Before(cutoff) })
		switch {
		case i >= len(tape):
			delete(d.tapes, sym)
		case i > 0:
			d.tapes[sym] = append([]Print(nil), tape[i:]...)
		}
	}
}

// TapeLen возвращает число принтов в ленте тикера (для диагностики и тестов).
func (d *Detector) TapeLen(symbol string) int { return len(d.tapes[symbol]) }

// Symbols — инструменты, по которым сейчас есть лента, по алфавиту.
func (d *Detector) Symbols() []string {
	out := make([]string, 0, len(d.tapes))
	for sym := range d.tapes {
		out = append(out, sym)
	}
	sort.Strings(out)
	return out
}

// Heads — «часы ленты»: до какого момента дошла лента каждого тикера. По ним, а
// не по стенным часам, считаются пропущенные роботом такты — стенные часы обгоняют
// ленту на минуты запаздывания фида.
//
// Верхняя граница та же, что у окна анализа: принты свежее now детектор в разбор
// не берёт, и засчитывать по ним пропуск нельзя — такт ещё не рассматривался.
func (d *Detector) Heads(now time.Time) map[string]time.Time {
	out := make(map[string]time.Time, len(d.tapes))
	for sym, tape := range d.tapes {
		w := d.window(tape, now)
		if len(w) > 0 {
			out[sym] = w[len(w)-1].Time
		}
	}
	return out
}

// BeatTol — допуск на один такт: настолько принт может отстать от сетки периода,
// оставаясь тем же роботом. Считается по худшему источнику (секундная метка ISS):
// реестр сессий по нему решает, пропустил ли робот такт, и ошибаться там лучше
// в сторону терпимости.
func (d *Detector) BeatTol(periodSec float64) time.Duration {
	return beatTol(d.cfg, periodSec, time.Second)
}

// beatTol — тот же допуск, с каким fitGrid принимает принт на такт сетки.
//
// Абсолютная часть покрывает округление биржевой метки времени: публичный фид ISS
// пишет TRADETIME с точностью до секунды, и принт робота приезжает на полсекунды
// раньше или позже своего места. Относительная растёт вместе с периодом.
//
// Сверху допуск ограничен четвертью периода. Без этого потолка на быстром роботе
// окно приёма накрывало почти весь такт (при периоде 3 с и допуске 1.2 с — 80 %
// времени), сетка переставала что-либо различать, и подгонка сваливалась в
// случайную кратность: одна и та же серия FLOT на ленте 28.08 определялась то как
// 3 с, то как 5, 6 или 12 — в зависимости от допуска, а не от робота.
func beatTol(cfg Config, periodSec float64, grain time.Duration) time.Duration {
	tol := cfg.PeriodTolAbs + grain/2
	if rel := time.Duration(cfg.PeriodTolRel * periodSec * float64(time.Second)); rel > tol {
		tol = rel
	}
	if cap := time.Duration(maxTolShare * periodSec * float64(time.Second)); tol > cap {
		tol = cap
	}
	return tol
}

// maxTolShare — какую долю такта разрешено занимать окну приёма (см. beatTol).
const maxTolShare = 0.25

// timeGrain — шаг биржевой метки времени в ленте. Секунда, если ни у одного принта
// нет долей секунды: так пишет время публичный фид ISS. Ноль означает, что источник
// отдаёт время точнее, и допуск на такт можно не раздувать под чужое округление.
func timeGrain(tape []Print) time.Duration {
	for _, p := range tape {
		if p.Time.Nanosecond() != 0 {
			return 0
		}
	}
	return time.Second
}

// Scan прогоняет детектор по всем лентам и возвращает найденных роботов,
// отсортированных по убыванию уверенности.
func (d *Detector) Scan(now time.Time) []Robot {
	d.Trim(now)
	var out []Robot
	for sym, tape := range d.tapes {
		out = append(out, d.scanTape(sym, d.window(tape, now))...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out
}

// scanTape ищет роботов в ленте одного тикера: отдельно по покупкам и по продажам,
// потому что противоположные направления — это разные роботы (или разные ноги одного).
func (d *Detector) scanTape(symbol string, tape []Print) []Robot {
	th := d.thresholdsFor(symbol)
	th.grain = timeGrain(tape)
	// Робот отправляет приказ, а лента печатает сделки, на которые он раскрошился
	// о стакан. Ищем по приказам: в сырых сделках лотовки робота нет вовсе.
	if d.cfg.AggressorGap > 0 {
		tape = mergeAggressors(tape, d.cfg.AggressorGap, d.cfg.AggressorSpan)
	}
	bySide := map[Side][]Print{}
	for _, p := range tape {
		if p.Side != SideBuy && p.Side != SideSell {
			continue
		}
		bySide[p.Side] = append(bySide[p.Side], p)
	}
	var out []Robot
	for side, prints := range bySide {
		out = append(out, d.scanSide(symbol, side, prints, th)...)
	}
	return out
}

// scanSide перебирает кластеры одинаковой лотовки и проверяет каждый на периодичность.
func (d *Detector) scanSide(symbol string, side Side, prints []Print, th thresholds) []Robot {
	if len(prints) < th.minPrints {
		return nil
	}

	// Кластеры строим по отсортированному объёму: кластер — это все принты, чья
	// лотовка укладывается в допуск от нижней границы. Скользим нижней границей по
	// каждому значению, чтобы не разрезать настоящую серию произвольной сеткой.
	byQty := append([]Print(nil), prints...)
	sort.Slice(byQty, func(i, j int) bool { return byQty[i].Qty < byQty[j].Qty })

	var out []Robot
	prevEnd := -1
	for i := 0; i < len(byQty); i++ {
		if i > 0 && byQty[i].Qty == byQty[i-1].Qty {
			continue // одинаковая лотовка даёт тот же кластер
		}
		limit := byQty[i].Qty + d.qtyWidth(byQty[i].Qty)
		end := sort.Search(len(byQty), func(j int) bool { return byQty[j].Qty > limit }) - 1
		if end-i+1 < th.minPrints {
			continue
		}
		if end == prevEnd {
			continue // подмножество уже разобранного кластера
		}
		prevEnd = end

		out = append(out, d.analyzeCluster(symbol, side, byQty[i:end+1], th)...)
	}
	return dedupe(out)
}

// qtyWidth — ширина кластера лотовки. Допуск двусторонний (±QtyTolAbs вокруг
// размера робота), а кластер отсчитывается от своей нижней границы, поэтому в него
// укладываются два допуска: серия 3011–3013 при допуске ±1 — это один робот.
func (d *Detector) qtyWidth(qty float64) float64 {
	return 2 * math.Max(d.cfg.QtyTolAbs, qty*d.cfg.QtyTolRel)
}

// analyzeCluster проверяет группу принтов одной лотовки на равномерность по времени.
//
// Возвращает все подошедшие такты, а не один самый убедительный. Выбирать здесь
// нечем: серия с тактом 3 с ложится и на сетку 6 с, и на 12 с, и какая из подгонок
// наберёт больший вес — вопрос допуска, а не робота (замер на ленте 28.08: FLOT
// 26 л определялся как 3, 5, 6 или 12 секунд при разных допусках). Настоящий период
// — самый частый из кратных, и выбирает его dedupe, которому видны все варианты.
func (d *Detector) analyzeCluster(symbol string, side Side, cluster []Print, th thresholds) []Robot {
	byTime := append([]Print(nil), cluster...)
	sort.Slice(byTime, func(i, j int) bool { return byTime[i].Time.Before(byTime[j].Time) })

	times := make([]float64, len(byTime))
	for i, p := range byTime {
		times[i] = p.Time.Sub(byTime[0].Time).Seconds()
	}

	var out []Robot
	for _, fit := range d.fitPeriods(times, th) {
		if r, ok := robotOf(symbol, side, byTime, fit, th); ok {
			out = append(out, r)
		}
	}
	return out
}

// robotOf описывает робота по подгонке такта: в серию попадают только принты,
// легшие на сетку.
func robotOf(symbol string, side Side, byTime []Print, fit periodFit, th thresholds) (Robot, bool) {
	// Кластер собирался по допуску вокруг лотовки и мог зацепить посторонние сделки
	// похожего размера. Робота описываем только теми принтами, что реально легли на
	// сетку такта, — иначе в «лотовке 3011–3013» оказался бы случайный сосед на 3006.
	series := make([]Print, 0, len(byTime))
	for i, p := range byTime {
		if fit.onBeat[i] {
			series = append(series, p)
		}
	}
	if len(series) < th.minPrints {
		return Robot{}, false
	}

	// На минимальной серии такт пропускать нельзя: «четыре повторяющихся принта» —
	// это четыре подряд идущих такта. Разрешив короткой серии пропуски, детектор
	// начинает собирать «роботов» из случайных сделок, раскладывая их по кратным
	// тактам, — на случайной ленте так находился робот в трети прогонов.
	if len(series) < ConfidentPrints && fit.hits != fit.beats {
		return Robot{}, false
	}

	qtys := make([]float64, len(series))
	trades := make([]float64, len(series))
	for i, p := range series {
		qtys[i] = p.Qty
		trades[i] = float64(p.Trades)
	}
	sort.Float64s(qtys)
	sort.Float64s(trades)

	r := Robot{
		Symbol: symbol,
		Side:   side,
		// Границы лотовки берём робастные (5-й и 95-й процентиль): один случайно
		// попавший на такт чужой принт не должен растягивать отображаемый диапазон.
		QtyMin:     percentile(qtys, 0.05),
		QtyMax:     percentile(qtys, 0.95),
		QtyTypical: median(qtys),
		PeriodSec:  fit.period,
		Jitter:     fit.jitter,
		Prints:     len(series),
		Beats:      fit.beats,
		Hits:       fit.hits,
		Confidence: fit.confidence,
		// Короткая серия показывается сразу, но честно помечается предварительной.
		Provisional: len(series) < ConfidentPrints,
		FirstSeen:   series[0].Time,
		LastSeen:    series[len(series)-1].Time,
		PriceFirst:  series[0].Price,
		PriceLast:   series[len(series)-1].Price,
		PrintTrades: median(trades),
	}
	return r, true
}

// periodFit — результат подгонки сетки такта под моменты принтов кластера.
type periodFit struct {
	period float64
	// beats — сколько тактов уложилось в найденную серию, от первого её принта
	// до последнего (включая пропущенные).
	beats int
	// hits — сколько тактов серии реально заняты принтами.
	hits int
	// matched — сколько принтов легло на сетку внутри серии.
	matched    int
	jitter     float64
	confidence float64
	// score — по нему выбирается лучшая гипотеза периода среди кандидатов.
	score float64
	// onBeat[i] — принт i лежит на сетке найденной серии.
	onBeat []bool
}

// fitPeriods подбирает такт под моменты принтов (в секундах от начала кластера).
//
// Разбор идёт не по соседним интервалам, а по сетке. Причина в том, как выглядит
// настоящая лента: кластер лотовки собирает не только принты робота, но и чужие
// сделки того же размера, и они встают между тактами. Считая соседние интервалы,
// детектор видел вместо такта 30 с пару «12 с и 18 с» — ни один из них в период не
// ложился, и робот пропадал тем вернее, чем длиннее окно и плотнее бумага (замер
// на ленте 28.08: FLOT и TATNP находились в окне 20 минут и терялись в 40).
//
// Сетка на этот шум не реагирует: принты робота остаются на своих местах по модулю
// периода, чужие рассыпаны равномерно и в фазу не попадают. Зато появляется своя
// опасность — на плотной ленте случайные сделки сами наберут любую сетку, поэтому
// найденная серия проверяется на неслучайность (см. fitGrid).
//
// Возвращаются все гипотезы, прошедшие проверки, включая кратные друг другу:
// какая из них настоящий период робота, решается уже на уровне тикера (dedupe).
func (d *Detector) fitPeriods(times []float64, th thresholds) []periodFit {
	if len(times) < th.minPrints {
		return nil
	}
	var out []periodFit
	add := func(seed float64) bool {
		fit, ok := d.fitGrid(times, seed, th)
		if !ok {
			return false
		}
		// Разные затравки часто сходятся к одному и тому же такту — но не к одной и
		// той же серии: подгонка от неточной затравки цепляется за ближний кусок
		// ленты и обрывается на нём. Из совпавших по периоду держим ту, что объяснила
		// больше ударов (замер на ленте 28.08: по PLZL затравка ровно 30 с давала
		// 8 тактов, уточнённая 30.13 — 32, и без этой замены страница показывала
		// удвоенный такт).
		for i, kept := range out {
			if math.Abs(kept.period-fit.period) <= 0.02*fit.period {
				if fit.hits > kept.hits {
					out[i] = fit
				}
				return false
			}
		}
		out = append(out, fit)
		return true
	}

	seeds := d.periodSeeds(times, th)
	for _, seed := range seeds {
		add(seed)
	}
	// Подгонка от затравки сходится к ближайшей сетке, а ближайшей нередко
	// оказывается кратная: серия с тактом 30 с ложится на сетку 60 с, и уточнение
	// уводит период туда же. Поэтому у каждой принятой гипотезы отдельно проверяются
	// её доли: настоящий период объяснит вдвое больше ударов и выиграет в dedupe,
	// а если робота с таким тактом нет — доля не пройдёт порог занятости.
	for i := 0; i < len(out); i++ {
		for _, k := range []float64{2, 3} {
			add(out[i].period / k)
		}
	}
	return out
}

// maxSeeds — сколько гипотез периода проверяется на кластер. Гипотезы отсортированы
// по частоте соответствующего интервала, и дальше первых нескольких идёт уже хвост
// случайных совпадений: перебирать его — только тратить время на каждом скане.
const maxSeeds = 8

// periodSeeds — гипотезы такта, отобранные по частоте интервалов кластера.
//
// Берутся интервалы между соседними принтами, поделённые на 1..MaxSkip тактов
// (робот пропускает такты, и тогда соседний интервал — это два-три периода), и
// умноженные на 2 и 3 (между тактами робота встаёт чужая сделка, и интервал,
// наоборот, дробится). Гипотезы огрубляются до допуска на такт и считаются по
// частоте: чем чаще интервал повторяется, тем больше похож на период.
func (d *Detector) periodSeeds(times []float64, th thresholds) []float64 {
	minP := d.cfg.MinPeriod.Seconds()
	maxP := d.cfg.MaxPeriod.Seconds()

	counts := make(map[int64]int, 64)
	repr := make(map[int64]float64, 64)
	note := func(p float64) {
		if p < minP || p > maxP {
			return
		}
		key := int64(p / beatTol(d.cfg, p, th.grain).Seconds())
		counts[key]++
		if _, ok := repr[key]; !ok {
			repr[key] = p
		}
	}
	for i := 1; i < len(times); i++ {
		dt := times[i] - times[i-1]
		if dt <= 0 {
			continue
		}
		for k := 1; k <= d.cfg.MaxSkip; k++ {
			note(dt / float64(k))
		}
		note(dt * 2)
		note(dt * 3)
	}
	if len(counts) == 0 {
		return nil
	}

	keys := make([]int64, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return repr[keys[i]] < repr[keys[j]]
	})
	if len(keys) > maxSeeds {
		keys = keys[:maxSeeds]
	}
	out := make([]float64, 0, len(keys))
	for _, k := range keys {
		seed := repr[k]
		// Допуск на этом шаге шире рабочего ровно на шаг метки времени: гипотеза
		// пришла из одного огрублённого интервала и промахивается на полшага, а
		// собрать вокруг неё нужно все интервалы серии — и четырёхсекундные, и
		// пятисекундные, если робот на самом деле бьёт через 4.3 с.
		seedTol := beatTol(d.cfg, seed, th.grain).Seconds() + th.grain.Seconds()
		out = append(out, refineSeed(times, seed, seedTol, d.cfg.MaxSkip))
	}
	return out
}

// refineSeed уточняет гипотезу такта по всем интервалам, которые на неё легли.
//
// Отдельный интервал приходит огрублённым: секундная метка ISS превращает период
// 4.3 с в чередование четырёх и пяти секунд, и любая одиночная гипотеза промахивается
// на полсекунды. На длинной серии промах накапливается — к восьмидесятому такту
// сетка уезжает на двадцать секунд и уже ничего не ловит. Среднее по всем интервалам
// (сумма длительностей на сумму тактов) снимает огрубление сразу: сотые доли секунды
// восстанавливаются по десяткам интервалов.
func refineSeed(times []float64, seed, tol float64, maxSkip int) float64 {
	var sumD, sumK float64
	for i := 1; i < len(times); i++ {
		dt := times[i] - times[i-1]
		if dt <= 0 {
			continue
		}
		k := math.Round(dt / seed)
		if k < 1 || int(k) > maxSkip {
			continue
		}
		if math.Abs(dt-k*seed) > tol*k {
			continue
		}
		sumD += dt
		sumK += k
	}
	if sumK == 0 {
		return seed
	}
	return sumD / sumK
}

// fitGrid подгоняет сетку такта под моменты принтов, стартуя с гипотезы seed.
//
// Три прохода: свёртка по модулю периода даёт фазу, приписывание принтов к тактам —
// серию, метод наименьших квадратов по номерам тактов — уточнённые период и фазу.
// Дальше из принтов на сетке вырезается непрерывная серия (пропуск больше MaxSkip
// тактов подряд разрывает её) и проверяется на неслучайность.
func (d *Detector) fitGrid(times []float64, seed float64, th thresholds) (periodFit, bool) {
	period := seed
	tol := beatTol(d.cfg, period, th.grain).Seconds()
	phase := bestPhase(times, period, tol)

	var ns []int
	var idx []int
	// Приписывает принты к тактам сетки при текущих периоде и фазе.
	assign := func() bool {
		tol = beatTol(d.cfg, period, th.grain).Seconds()
		ns, idx = ns[:0], idx[:0]
		for i, t := range times {
			n := math.Round((t - phase) / period)
			if math.Abs(t-phase-n*period) > tol {
				continue
			}
			ns = append(ns, int(n))
			idx = append(idx, i)
		}
		return len(ns) >= th.minPrints
	}

	for pass := 0; pass < 3; pass++ {
		if !assign() {
			return periodFit{}, false
		}
		period, phase = fitLine(ns, idx, times, period, phase)
		if period < d.cfg.MinPeriod.Seconds() || period > d.cfg.MaxPeriod.Seconds() {
			return periodFit{}, false
		}
	}
	// Последнее приписывание — уже по уточнённым периоду и фазе: иначе серия
	// описывалась бы разбивкой, снятой до последнего уточнения, и часть принтов
	// робота в неё не попадала.
	if !assign() {
		return periodFit{}, false
	}

	from, to, ok := longestRun(ns, d.cfg.MaxSkip)
	if !ok {
		return periodFit{}, false
	}

	tol = beatTol(d.cfg, period, th.grain).Seconds()
	onBeat := make([]bool, len(times))
	var resid []float64
	hits, matched := 0, 0
	prevBeat := math.MinInt32
	for i := from; i <= to; i++ {
		onBeat[idx[i]] = true
		matched++
		resid = append(resid, times[idx[i]]-phase-float64(ns[i])*period)
		if ns[i] != prevBeat {
			hits++
			prevBeat = ns[i]
		}
	}
	beats := ns[to] - ns[from] + 1
	// Порог меряется ударами, а не принтами. Три сделки, две из которых пришлись
	// на один такт, — это два удара и один интервал между ними, подтверждать период
	// там нечем. На ленте 28.08 такие «роботы» составляли треть трёхпринтовых
	// находок и все до одного были совпадениями.
	if hits < th.minPrints || matched < th.minPrints || beats < th.minBeats {
		return periodFit{}, false
	}

	occupancy := float64(hits) / float64(beats)
	if occupancy < d.cfg.MinOccupancy {
		return periodFit{}, false
	}

	jitter := stddev(resid) / period
	maxJitter := maxJitterFor(d.cfg, period, th.grain)
	if jitter > maxJitter {
		return periodFit{}, false
	}

	// Проверка на случайность: сколько принтов легло бы на эту сетку само собой.
	//
	// Сетка занимает не всё время серии, а только окна ±tol вокруг тактов — их
	// суммарная доля и есть покрытие. Если робота нет, принты кластера разложены по
	// времени как попало, и в окна их попадёт ровно доля покрытия. Серия принимается,
	// когда тактов занято заметно больше этого.
	//
	// Кластер, в котором нет ничего, кроме самой серии, проверять нечем и незачем:
	// постороннего потока, из которого могло бы случайно сложиться такое совпадение,
	// в нём просто нет. Это и делает возможным валютный порог в два принта — там
	// принимается только одинокая пара, а такая же пара внутри плотного потока
	// сделок того же размера отсеивается.
	span := times[idx[to]] - times[idx[from]]
	var inSpan int
	for _, t := range times {
		if t >= times[idx[from]] && t <= times[idx[to]] {
			inSpan++
		}
	}
	coverage := float64(beats) * 2 * tol / span
	if span > 0 && coverage < 1 && inSpan > matched {
		expected := float64(inSpan) * coverage
		if float64(hits)-expected < d.cfg.NoiseSigma*math.Sqrt(expected) {
			return periodFit{}, false
		}
	}

	fit := periodFit{
		period:  period,
		beats:   beats,
		hits:    hits,
		matched: matched,
		jitter:  jitter,
		onBeat:  onBeat,
	}
	fit.confidence = confidence(occupancy, hits, jitter, maxJitter)
	// Сравнивая гипотезы, предпочитаем ту, что объясняет больше принтов ровнее.
	fit.score = float64(hits) * occupancy * (1 - jitter/d.cfg.MaxJitter)
	return fit, true
}

// bestPhase — фаза сетки: моменты сворачиваются по модулю периода, и берётся
// самый населённый бин вместе с соседями (сетка может лечь на границу бина).
func bestPhase(times []float64, period, tol float64) float64 {
	if period <= 0 {
		return 0
	}
	bins := int(math.Ceil(period / math.Max(tol, 1e-6)))
	if bins < 1 {
		bins = 1
	}
	if bins > maxPhaseBins {
		bins = maxPhaseBins
	}
	counts := make([]int, bins)
	width := period / float64(bins)
	for _, t := range times {
		b := int(math.Mod(math.Mod(t, period)+period, period) / width)
		if b >= bins {
			b = bins - 1
		}
		counts[b]++
	}
	best, bestSum := 0, -1
	for b := range counts {
		s := counts[b] + counts[(b+1)%bins] + counts[(b+bins-1)%bins]
		if s > bestSum {
			best, bestSum = b, s
		}
	}
	return (float64(best) + 0.5) * width
}

// maxPhaseBins ограничивает свёртку: на длинном периоде с мелким допуском бинов
// набралось бы столько, что скан упёрся бы в них, а не в ленту.
const maxPhaseBins = 4096

// maxJitterFor — предел разброса принтов вокруг сетки, свой для каждого источника.
//
// Настоящий робот бьёт по своим часам, и на потоке с миллисекундной меткой его
// разброс — сотые доли такта. Единый щедрый предел был калиброван под ленту ISS,
// где время округлено до секунды: там разброс не меньше самого округления и на
// коротком такте занимает заметную долю периода. На быстрой ленте тот же предел
// пропускал случайные сетки: плотный поток однолотовых сделок ложился на «такт»
// с разбросом 0.09 и объявлялся роботом.
//
// Поэтому к базовому пределу добавляется ровно та часть, которую вносит округление
// источника: у равномерной ошибки в шаг метки среднеквадратичное отклонение —
// шаг, делённый на корень из двенадцати.
func maxJitterFor(cfg Config, periodSec float64, grain time.Duration) float64 {
	if periodSec <= 0 {
		return cfg.MaxJitter
	}
	quantization := grain.Seconds() / math.Sqrt(12) / periodSec
	return cfg.MaxJitter + quantization
}

// fitLine уточняет период и фазу методом наименьших квадратов по номерам тактов:
// момент принта — линейная функция номера его такта, наклон и есть период.
func fitLine(ns, idx []int, times []float64, period, phase float64) (float64, float64) {
	var sn, st, snt, snn float64
	n := float64(len(ns))
	for i := range ns {
		x := float64(ns[i])
		y := times[idx[i]]
		sn += x
		st += y
		snt += x * y
		snn += x * x
	}
	den := n*snn - sn*sn
	if den == 0 {
		return period, phase
	}
	p := (n*snt - sn*st) / den
	if p <= 0 {
		return period, phase
	}
	return p, (st - p*sn) / n
}

// longestRun — самый длинный кусок серии без разрыва: соседние занятые такты не
// расходятся больше чем на maxSkip. Возвращает границы в срезе номеров тактов.
func longestRun(ns []int, maxSkip int) (int, int, bool) {
	if len(ns) == 0 {
		return 0, 0, false
	}
	if maxSkip < 1 {
		maxSkip = 1
	}
	bestFrom, bestTo := 0, 0
	from := 0
	for i := 1; i < len(ns); i++ {
		if ns[i]-ns[i-1] > maxSkip {
			if i-1-from > bestTo-bestFrom {
				bestFrom, bestTo = from, i-1
			}
			from = i
		}
	}
	if len(ns)-1-from > bestTo-bestFrom {
		bestFrom, bestTo = from, len(ns)-1
	}
	return bestFrom, bestTo, true
}

// confidence сводит три признака в одну оценку 0..1: насколько плотно заняты такты
// серии; её длина (после ~20 тактов совпадение уже не может быть случайным); и
// близость разброса к нулю.
func confidence(occupancy float64, hits int, jitter, maxJitter float64) float64 {
	length := math.Min(1, float64(hits)/20)
	tight := 1 - jitter/maxJitter
	if tight < 0 {
		tight = 0
	}
	c := occupancy * (0.5 + 0.5*length) * (0.5 + 0.5*tight)
	return math.Max(0, math.Min(1, c))
}

// dedupe убирает кластеры-двойники: соседние границы по объёму часто дают одного
// и того же робота, из совпадающих оставляем самого уверенного.
//
// Помимо точных двойников снимаются кратные: серия с тактом 30 с ложится и на
// сетку 60, и на 90 секунд — каждый второй, каждый третий её принт. Без этой
// проверки один робот показывался страницей несколько раз с разными таймингами
// (замер на ленте 28.08: GAZP 371 л шёл и как «30 с», и как «90 с», HEAD 7 л —
// как «16 с» и «32 с»). Из кратных оставляем самый частый такт: он и есть
// настоящий период, длинные — его гармоники.
func dedupe(in []Robot) []Robot {
	if len(in) < 2 {
		return in
	}
	sort.Slice(in, func(i, j int) bool {
		if in[i].Confidence != in[j].Confidence {
			return in[i].Confidence > in[j].Confidence
		}
		return in[i].PeriodSec < in[j].PeriodSec
	})
	var out []Robot
	for _, r := range in {
		// Совпавших может быть сразу несколько: такты 32 и 48 друг другу не кратны,
		// но оба — гармоники одного робота с тактом 16. Схлопываем всю группу разом,
		// иначе первый занявший место такт защищал бы от вытеснения второй.
		best := r
		kept := out[:0]
		for _, k := range out {
			if SameRobot(k, r) || harmonic(k, r) {
				if better(k, best) {
					best = k
				}
				continue
			}
			kept = append(kept, k)
		}
		out = append(kept, best)
	}
	return out
}

// better — какая из двух кратных подгонок ближе к настоящему такту робота.
//
// Решает охват: настоящий период объясняет все удары робота, а удвоенный — только
// каждый второй. Обратной опасности, что победит слишком частый такт, нет: сетка
// вдвое чаще настоящей наполовину пуста и не проходит порог занятости ещё в
// fitGrid. При равном охвате берём ту, где такты заняты плотнее.
//
// Занятость сама по себе решать не может: робот, пропускающий такты (на ленте
// 28.08 FLOT печатал 16 лотов через раз), на своём периоде набирает занятость
// ниже, чем на утроенном, — и по ней утроенный такт выигрывал бы у настоящего.
func better(a, b Robot) bool {
	if a.Hits != b.Hits {
		return a.Hits > b.Hits
	}
	return occupancy(a) > occupancy(b)
}

// occupancy — доля тактов серии, занятых принтами.
func occupancy(r Robot) float64 {
	if r.Beats <= 0 {
		return 0
	}
	return float64(r.Hits) / float64(r.Beats)
}

// maxHarmonic — до какой кратности такт считается гармоникой того же робота.
// Дальше идут периоды, которые уже не отличить от самостоятельного медленного
// робота той же лотовки.
const maxHarmonic = 6

// harmonic — описывают ли находки один и тот же робот, но разными кратностями
// такта: тот же тикер, направление и пересекающиеся лотовки, а периоды относятся
// как небольшие целые числа.
//
// Простой кратности мало. Робот с тактом 3 с на секундной метке ISS различим плохо,
// и подгонка садится то на 6 с, то на 9 с — друг другу они не кратны, но описывают
// одного и того же робота, и без этой проверки он показывался бы страницей дважды
// с разными таймингами.
func harmonic(a, b Robot) bool {
	if a.Symbol != b.Symbol || a.Side != b.Side {
		return false
	}
	if a.QtyMin > b.QtyMax || b.QtyMin > a.QtyMax {
		return false
	}
	lo, hi := a.PeriodSec, b.PeriodSec
	if lo > hi {
		lo, hi = hi, lo
	}
	if lo <= 0 {
		return false
	}
	for m := 1; m <= maxRatioDen; m++ {
		k := math.Round(hi * float64(m) / lo)
		if k <= float64(m) || k > maxHarmonic*float64(m) {
			continue
		}
		// Тот же допуск, с каким детектор раскладывал принты по тактам: иначе
		// «кратным» объявлялся бы любой период, случайно оказавшийся рядом.
		if math.Abs(hi*float64(m)-k*lo) <= 0.05*hi*float64(m) {
			return true
		}
	}
	return false
}

// maxRatioDen — максимальный знаменатель отношения периодов, при котором такты
// считаются кратностями одного робота (3/2, 4/3 и подобные).
const maxRatioDen = 4

// SameRobot решает, описывают ли две находки одного и того же робота: тот же
// тикер и направление, пересекающиеся лотовки и период в пределах 10%.
// Используется и при дедупликации скана, и при продолжении сессии робота во времени.
func SameRobot(a, b Robot) bool {
	if a.Symbol != b.Symbol || a.Side != b.Side {
		return false
	}
	if a.QtyMin > b.QtyMax || b.QtyMin > a.QtyMax {
		return false
	}
	ref := math.Max(a.PeriodSec, b.PeriodSec)
	return math.Abs(a.PeriodSec-b.PeriodSec) <= 0.1*ref
}

func median(sorted []float64) float64 { return percentile(sorted, 0.5) }

// percentile возвращает значение квантиля q в уже отсортированном срезе.
func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q * float64(len(sorted)-1))
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

func stddev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(len(xs))
	var acc float64
	for _, x := range xs {
		acc += (x - mean) * (x - mean)
	}
	return math.Sqrt(acc / float64(len(xs)-1))
}
