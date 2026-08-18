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

// thresholds — пороги длины серии для конкретного инструмента.
type thresholds struct {
	minPrints int
	minBeats  int
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
// оставаясь тем же роботом.
func (d *Detector) BeatTol(periodSec float64) time.Duration {
	return beatTol(d.cfg, periodSec)
}

// beatTol — тот же допуск, что fitPeriod применяет к отдельному интервалу.
func beatTol(cfg Config, periodSec float64) time.Duration {
	rel := time.Duration(cfg.PeriodTolRel * periodSec * float64(time.Second))
	if rel > cfg.PeriodTolAbs {
		return rel
	}
	return cfg.PeriodTolAbs
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

		if r, ok := d.analyzeCluster(symbol, side, byQty[i:end+1], th); ok {
			out = append(out, r)
		}
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
func (d *Detector) analyzeCluster(symbol string, side Side, cluster []Print, th thresholds) (Robot, bool) {
	byTime := append([]Print(nil), cluster...)
	sort.Slice(byTime, func(i, j int) bool { return byTime[i].Time.Before(byTime[j].Time) })

	times := make([]float64, len(byTime))
	for i, p := range byTime {
		times[i] = p.Time.Sub(byTime[0].Time).Seconds()
	}

	fit, ok := d.estimatePeriod(times, th)
	if !ok {
		return Robot{}, false
	}

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

	// На минимальной серии такт пропускать нельзя: «три повторяющихся принта» —
	// это три подряд идущих такта. Разрешив короткой серии пропуски, детектор
	// начинает собирать «роботов» из случайных сделок, раскладывая их по кратным
	// тактам, — на случайной ленте так находился робот в трети прогонов.
	if len(series) < ConfidentPrints && fit.beats != fit.matched {
		return Robot{}, false
	}

	qtys := make([]float64, len(series))
	for i, p := range series {
		qtys[i] = p.Qty
	}
	sort.Float64s(qtys)

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
		Confidence: fit.confidence,
		// Короткая серия показывается сразу, но честно помечается предварительной.
		Provisional: len(series) < ConfidentPrints,
		FirstSeen:  series[0].Time,
		LastSeen:   series[len(series)-1].Time,
		PriceFirst: series[0].Price,
		PriceLast:  series[len(series)-1].Price,
	}
	return r, true
}

// periodFit — результат подгонки периода под ряд интервалов.
type periodFit struct {
	period     float64
	beats      int
	matched    int
	jitter     float64
	confidence float64
	// onBeat[i] — принт i участвует хотя бы в одном интервале, легшем на такт.
	onBeat []bool
}

// estimatePeriod подбирает период под моменты принтов (в секундах от начала серии).
//
// Прямое усреднение интервалов не годится: биржевая метка времени дискретна по
// секунде, поэтому период 11.2 с приходит как чередование 11 и 12. Поэтому каждый
// интервал сопоставляется с целым числом тактов k, а период считается как
// Σинтервалов / Σтактов — по длинной серии это даёт доли секунды точности.
//
// Гипотезы периода берём из квартилей интервалов: медиана ломается, если внутри
// кластера половина принтов — посторонний шум того же размера.
func (d *Detector) estimatePeriod(times []float64, th thresholds) (periodFit, bool) {
	if len(times) < th.minPrints {
		return periodFit{}, false
	}
	deltas := make([]float64, 0, len(times)-1)
	for i := 1; i < len(times); i++ {
		deltas = append(deltas, times[i]-times[i-1])
	}

	sorted := make([]float64, 0, len(deltas))
	for _, dt := range deltas {
		if dt > 0 {
			sorted = append(sorted, dt)
		}
	}
	if len(sorted) < th.minBeats {
		return periodFit{}, false
	}
	sort.Float64s(sorted)

	minP := d.cfg.MinPeriod.Seconds()
	maxP := d.cfg.MaxPeriod.Seconds()

	var best periodFit
	var found bool
	for _, q := range []float64{0.25, 0.5, 0.75} {
		seed := percentile(sorted, q)
		if seed < minP || seed > maxP {
			continue
		}
		fit, ok := d.fitPeriod(deltas, seed, th)
		if !ok {
			continue
		}
		if !found || fit.confidence > best.confidence {
			best, found = fit, true
		}
	}
	return best, found
}

// fitPeriod уточняет период, стартуя с гипотезы seed. Два прохода: первый
// раскладывает интервалы по тактам грубой гипотезы, второй — уже по уточнённой.
func (d *Detector) fitPeriod(deltas []float64, seed float64, th thresholds) (periodFit, bool) {
	period := seed
	var matched, beats, unitBeats int
	var norms []float64
	onBeat := make([]bool, len(deltas)+1)

	for pass := 0; pass < 2; pass++ {
		matched, beats, unitBeats = 0, 0, 0
		norms = norms[:0]
		for i := range onBeat {
			onBeat[i] = false
		}
		var sumD float64
		for i, dt := range deltas {
			k := math.Round(dt / period)
			if k < 1 || int(k) > d.cfg.MaxSkip {
				continue
			}
			expected := k * period
			tol := math.Max(d.cfg.PeriodTolAbs.Seconds(), d.cfg.PeriodTolRel*expected)
			if math.Abs(dt-expected) > tol {
				continue
			}
			matched++
			beats += int(k)
			if k == 1 {
				unitBeats++
			}
			sumD += dt
			norms = append(norms, dt/k)
			onBeat[i], onBeat[i+1] = true, true
		}
		if beats == 0 {
			return periodFit{}, false
		}
		period = sumD / float64(beats)
	}

	if matched < th.minBeats {
		return periodFit{}, false
	}
	if float64(unitBeats) < d.cfg.MinUnitBeatRatio*float64(matched) {
		return periodFit{}, false
	}
	if period < d.cfg.MinPeriod.Seconds() || period > d.cfg.MaxPeriod.Seconds() {
		return periodFit{}, false
	}
	ratio := float64(matched) / float64(len(deltas))
	if ratio < d.cfg.MinMatchRatio {
		return periodFit{}, false
	}
	jitter := stddev(norms) / period
	if jitter > d.cfg.MaxJitter {
		return periodFit{}, false
	}

	return periodFit{
		period:     period,
		beats:      beats,
		matched:    matched,
		jitter:     jitter,
		confidence: confidence(ratio, matched, jitter, d.cfg.MaxJitter),
		onBeat:     onBeat,
	}, true
}

// confidence сводит три признака в одну оценку 0..1: доля интервалов, легших в
// период; длина серии (после ~20 тактов совпадение уже не может быть случайным);
// и близость разброса к нулю.
func confidence(ratio float64, matched int, jitter, maxJitter float64) float64 {
	length := math.Min(1, float64(matched)/20)
	tight := 1 - jitter/maxJitter
	if tight < 0 {
		tight = 0
	}
	c := ratio * (0.5 + 0.5*length) * (0.5 + 0.5*tight)
	return math.Max(0, math.Min(1, c))
}

// dedupe убирает кластеры-двойники: соседние границы по объёму часто дают одного
// и того же робота, из совпадающих оставляем самого уверенного.
func dedupe(in []Robot) []Robot {
	if len(in) < 2 {
		return in
	}
	sort.Slice(in, func(i, j int) bool { return in[i].Confidence > in[j].Confidence })
	var out []Robot
	for _, r := range in {
		dup := false
		for _, kept := range out {
			if SameRobot(kept, r) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, r)
		}
	}
	return out
}

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
