package funding

import (
	"math"
	"sync"
	"time"

	"github.com/funding-service/backend/internal/source"
)

var msk = time.FixedZone("MSK", 3*60*60)

// futuresOfficialSym maps futures symbol to the corresponding CBR official rate symbol.
var futuresOfficialSym = map[string]string{
	source.SymbolUSDRUBF: source.SymbolUSDRubOfficial,
	source.SymbolEURRUBF: source.SymbolEURRubOfficial,
}


// inFundingWindow reports whether t falls inside the 10:00–15:30 MSK VWAP window.
// Both legs of the MOEX funding formula are defined over exactly this window: the
// CBR fixing (spot TOM WAPRICE, CBR methodology) and the perpetual-futures leg
// (VWAP of on-book deals 10:00–15:30, MOEX methodology). Ticks outside the window
// must not enter either accumulator: the derivatives market trades from 07:00 MSK
// (ЕТС since 23.03.2026) and morning volume skews the VWAP off the exchange value.
func inFundingWindow(t time.Time) bool {
	h, m, _ := t.In(msk).Clock()
	if h < 10 {
		return false
	}
	if h < 15 {
		return true
	}
	return h == 15 && m < 30
}

// FundingSnapshot holds the latest computed values for all tracked instruments.
type FundingSnapshot struct {
	Timestamp    time.Time
	USDRUBF      InstrumentFunding
	EURRUBF      InstrumentFunding
	CNYRUBF      InstrumentFunding
	USDTRUBPrice float64
}

// InstrumentFunding holds VWAP, last price, and funding values for one instrument.
// Pointer fields are nil until the required data arrives.
type InstrumentFunding struct {
	VWAP             float64
	LastPrice        float64
	ForexFunding     *float64 // nil until USDCNH and CNYRUBF last price are both ingested
	MOEXFunding      *float64 // swap_rate from MOEX ISS; nil until first ISS poll returns SWAPRATE
	CBFunding        *float64 // clamp(settle_price − CBR_rate, K1, K2); non-nil once both settlement and CBR rate are available
	OfficialRate     *float64 // most recent CBR rate; nil until published
	PredictedFunding *float64 // live clamp(sessionVWAP_futures − predictedCBRate, K1·rate, K2·rate)
	PredictedCBRate  *float64 // live estimate of today's CBR fixing: VWAP of spot TOM over 10:00–15:30 MSK
}

// sessionAcc accumulates a volume-weighted sum over the 10:00–15:30 MSK funding
// window (ΔVOLTODAY approximation, fallback to the exact trade feed). Volumes from
// MOEX ISS are VOLTODAY (running total); we track deltas to get proper incremental
// weights. Out-of-window ticks only move the lastVol baseline so pre-10:00 and
// post-15:30 volume never enters the VWAP. The accumulator resets when the MSK
// date changes.
type sessionAcc struct {
	sumPV          float64 // Σ(price × ΔvolToday) over the funding window
	sumV           float64 // Σ(ΔvolToday) over the funding window
	lastVol        float64 // last observed VOLTODAY (to compute deltas)
	date           string  // MSK date "YYYY-MM-DD" of the current accumulation
	startedPre1530 bool    // true only if the first tick arrived before 15:30 MSK
}

func (a *sessionAcc) vwap() (float64, bool) {
	if a.sumV <= 0 {
		return 0, false
	}
	return a.sumPV / a.sumV, true
}

// tradeAcc accumulates the funding-window (10:00–15:30 MSK) VWAP from individual
// executed deals (KindTrade ticks). Unlike sessionAcc it needs no delta arithmetic:
// each tick already carries the volume of exactly one trade. dayV counts the whole
// day including out-of-window deals — completeness against VOLTODAY (a full-day
// total) must not be judged by the window-only volume.
type tradeAcc struct {
	sumPV          float64 // Σ(price × quantity) over the funding window
	sumV           float64 // Σ(quantity) over the funding window
	dayV           float64 // Σ(quantity) over the whole day (feed-completeness check)
	date           string  // MSK date "YYYY-MM-DD" of the current accumulation
	startedPre1530 bool    // first trade of the day was before 15:30 MSK (backfill covers the session start)
}

func (a *tradeAcc) vwap() (float64, bool) {
	if a.sumV <= 0 {
		return 0, false
	}
	return a.sumPV / a.sumV, true
}

// tradeFeedMinCompleteness is the fraction of marketdata VOLTODAY that the
// captured trade volume must cover for the trade feed to be considered
// authoritative. Below this the trades endpoint is lagging behind real volume
// (dead/slow while the future keeps trading) and the engine falls back to the
// ΔVOLTODAY approximation. The slack absorbs off-market deals (excluded from our
// sum but sometimes counted in VOLTODAY) and minor poll-timing skew.
const tradeFeedMinCompleteness = 0.90

// Engine ingests Ticks from any source and computes FundingSnapshots on demand.
// All fields are protected by mu; VWAPCalculators have their own internal mutexes.
type Engine struct {
	vwaps        map[string]*VWAPCalculator // 6-hour rolling VWAP for display (ΔVOLTODAY approximation, fallback)
	tradeVWAPs   map[string]*VWAPCalculator // 6-hour rolling VWAP from real deals (KindTrade, preferred)
	tradeAccs    map[string]*tradeAcc       // session VWAP from real deals (reset on MSK date change)
	lastPriceAt  map[string]time.Time       // timestamp of the newest KindLastPrice tick per symbol
	sessionAccs  map[string]*sessionAcc     // cumulative session VWAP (reset at MSK midnight)
	spotTOMWAP     map[string]float64       // WAPRICE for spot TOM frozen at 10:00–15:30 → best CB-fixing predictor
	spotTOMWAPLive map[string]float64       // latest WAPRICE for spot TOM (any time) → fallback so the predicted row is never empty on a late start
	settlVWAP        map[string]*float64        // sentinel: non-nil once settlement has occurred
	settlDate        string                     // MSK date for which settlement was recorded
	vwapLastVol      map[string]float64         // last VOLTODAY per symbol, to weight the rolling VWAP by ΔVOLTODAY
	lastPrice        map[string]float64
	swapRate         map[string]float64
	forexRates       map[string]float64
	eurUSDFix        float64 // EUR/USD «по состоянию на 15:30 МСК» — фиксинг ЕЦБ для расчёта курса EUR ЦБ (методика с 08.06.2026)
	eurUSDFixDate    string  // MSK-дата, за которую накоплен eurUSDFix (для суточного сброса)
	officialRate        map[string]float64
	officialRateDate    map[string]string  // MSK date when officialRate was last published
	officialRateAtSettl map[string]float64 // курс ЦБ, зафиксированный при settlement (15:30)
	rateEffectiveToday  map[string]float64 // засев из БД: курс ЦБ, ДЕЙСТВУЮЩИЙ сегодня (опубликован вчера); officialSym -> rate
	rateEffectiveDate   string             // MSK-дата, на которую действителен rateEffectiveToday
	mu               sync.Mutex

	// settlCh fires once per trading day when the first settlVWAP is frozen (~15:30).
	settlCh         chan time.Time
	settlFiredDate  string // MSK date on which the settlement signal was already sent
}

// NewEngine creates an Engine with a 6-hour rolling VWAP window.
func NewEngine() *Engine {
	futures := []string{source.SymbolUSDRUBF, source.SymbolEURRUBF, source.SymbolCNYRUBF}
	vwaps := make(map[string]*VWAPCalculator, len(futures))
	tradeVWAPs := make(map[string]*VWAPCalculator, len(futures))
	for _, sym := range futures {
		vwaps[sym] = NewVWAP(6 * time.Hour)
		tradeVWAPs[sym] = NewVWAP(6 * time.Hour)
	}
	return &Engine{
		vwaps:            vwaps,
		tradeVWAPs:       tradeVWAPs,
		tradeAccs:        make(map[string]*tradeAcc),
		lastPriceAt:      make(map[string]time.Time),
		sessionAccs:      make(map[string]*sessionAcc),
		spotTOMWAP:       make(map[string]float64),
		spotTOMWAPLive:   make(map[string]float64),
		settlVWAP:        make(map[string]*float64),
		vwapLastVol:      make(map[string]float64),
		lastPrice:        make(map[string]float64),
		swapRate:         make(map[string]float64),
		forexRates:       make(map[string]float64),
		officialRate:        make(map[string]float64),
		officialRateDate:    make(map[string]string),
		officialRateAtSettl: make(map[string]float64),
		rateEffectiveToday:  make(map[string]float64),
		settlCh:             make(chan time.Time, 1),
	}
}

// SettlementCh returns a channel that receives the time once per trading day
// when the first settlement VWAP is frozen (~15:30 MSK).
func (e *Engine) SettlementCh() <-chan time.Time { return e.settlCh }

// tryFireSettlSignal sends to settlCh once per MSK trading day.
// Must be called while holding e.mu.
func (e *Engine) tryFireSettlSignal(mskDate string) {
	if e.settlFiredDate == mskDate {
		return
	}
	e.settlFiredDate = mskDate
	select {
	case e.settlCh <- time.Now():
	default:
	}
}

// Ingest routes a tick to the appropriate internal cache or VWAP calculator.
func (e *Engine) Ingest(tick source.Tick) {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch tick.Symbol {
	case source.SymbolUSDRUBF, source.SymbolEURRUBF, source.SymbolCNYRUBF:
		switch tick.Kind {
		case source.KindLastPrice:
			// Weight the rolling VWAP by volume TRADED per update — the increment in
			// VOLTODAY (a running daily total) — not VOLTODAY itself. Feeding raw
			// VOLTODAY weights every tick by the whole day's cumulative volume, which
			// hugely overweights later prices and reseeds oddly on restart. Mirrors the
			// session accumulator. A non-positive delta (day rollover: VOLTODAY resets)
			// is skipped; the new baseline is stored for the next tick.
			if last, ok := e.vwapLastVol[tick.Symbol]; ok {
				if dv := tick.Volume - last; dv > 0 {
					e.vwaps[tick.Symbol].Add(tick.Price, dv, tick.Timestamp)
				}
			}
			e.vwapLastVol[tick.Symbol] = tick.Volume
			e.lastPrice[tick.Symbol] = tick.Price
			if tick.Timestamp.After(e.lastPriceAt[tick.Symbol]) {
				e.lastPriceAt[tick.Symbol] = tick.Timestamp
			}
			e.ingestSessionTick(tick)
		case source.KindTrade:
			e.ingestTradeTick(tick)
		case source.KindBid, source.KindAsk:
			e.lastPrice[tick.Symbol] = tick.Price
		case source.KindSwapRate:
			e.swapRate[tick.Symbol] = tick.Price
		case source.KindSettlePrice:
			// IGNORED as a settlement source. ISS puts the CURRENT price into
			// SETTLEPRICE after a restart (observed live 14.07: SETTLEPRICE 78.01 vs
			// LAST 78.06 while the true 15:30 VWAP was 77.17), and this tick races
			// ahead of the trades backfill, poisoning settlVWAP with an evening price.
			// The settlement VWAP is frozen at 15:30 exclusively by maybeFreezeSettl
			// from the trade/session accumulators — the trades backfill makes that
			// work even when the service (re)starts after 15:30.
		}

	case source.SymbolUSDRubTOM:
		// WAPRICE is the MOEX ISS session VWAP from market open (10:00 MSK), matching the
		// CBR official-rate methodology. The 10:00–15:30 value (spotTOMWAP) is the best
		// fixing predictor — frozen at the window close so post-15:30 trades don't skew it.
		// We also keep the latest WAPRICE (spotTOMWAPLive) regardless of time so the
		// predicted row falls back to a live value instead of being empty on a late start.
		if tick.Kind == source.KindWaprice {
			e.spotTOMWAPLive[tick.Symbol] = tick.Price
			if inFundingWindow(tick.Timestamp) {
				e.spotTOMWAP[tick.Symbol] = tick.Price
			}
		}

	case source.SymbolEURUSD, source.SymbolUSDCNH:
		e.forexRates[tick.Symbol] = tick.Price
		if tick.Symbol == source.SymbolEURUSD {
			e.freezeEURUSDFixing(tick)
		}

	case source.SymbolUSDRubOfficial, source.SymbolEURRubOfficial:
		e.officialRate[tick.Symbol] = tick.Price
		if tick.Kind == source.KindNewOfficialRate {
			e.officialRateDate[tick.Symbol] = tick.Timestamp.In(msk).Format("2006-01-02")
		}

	case source.SymbolUSDTRUB:
		e.lastPrice[tick.Symbol] = tick.Price
	}
}

// ingestSessionTick updates the cumulative session VWAP accumulator for a LastPrice tick.
// It detects daily rollovers via the MSK date on the tick timestamp and freezes the
// settlement sentinel (settlVWAP) at 15:30 MSK when the service has been running
// since before settlement (startedPre1530=true).
func (e *Engine) ingestSessionTick(tick source.Tick) {
	sym := tick.Symbol
	mskTime := tick.Timestamp.In(msk)
	mskDate := mskTime.Format("2006-01-02")

	acc := e.sessionAccs[sym]
	if acc == nil || acc.date != mskDate {
		// New trading day: clear settlement state for this symbol.
		if acc != nil {
			e.settlVWAP[sym] = nil
			delete(e.officialRateAtSettl, sym)
			if e.settlDate == acc.date {
				e.settlDate = ""
			}
		}
		// Bootstrap with an empty accumulator: the first tick's VOLTODAY is the
		// day-so-far total and includes pre-10:00 (ЕТС) volume, which must not be
		// attributed to the current price. It only sets the delta baseline; the
		// funding-window VWAP is built from in-window deltas alone.
		h0, m0, _ := mskTime.Clock()
		e.sessionAccs[sym] = &sessionAcc{
			lastVol:        tick.Volume,
			date:           mskDate,
			startedPre1530: h0 < 15 || (h0 == 15 && m0 < 30),
		}
		acc = e.sessionAccs[sym]
	} else {
		deltaVol := tick.Volume - acc.lastVol
		if deltaVol > 0 && inFundingWindow(mskTime) {
			acc.sumPV += tick.Price * deltaVol
			acc.sumV += deltaVol
		}
		acc.lastVol = tick.Volume
	}

	// Set the settlement sentinel at 15:30 MSK if not yet done for today.
	e.maybeFreezeSettl(sym, mskTime)
}

// ingestTradeTick feeds one executed deal (KindTrade) into the exact rolling
// VWAP and the trade-based session accumulator. Trades arrive in TRADENO order,
// including a session backfill after a restart, so by the time the first
// post-15:30 trade shows up the accumulator holds exactly the pre-settlement
// session VWAP — the freeze check runs BEFORE the trade is added.
func (e *Engine) ingestTradeTick(tick source.Tick) {
	if tick.Price <= 0 || tick.Volume <= 0 || tick.Timestamp.IsZero() {
		return
	}
	sym := tick.Symbol
	mskTime := tick.Timestamp.In(msk)
	mskDate := mskTime.Format("2006-01-02")

	acc := e.tradeAccs[sym]
	if acc == nil || acc.date != mskDate {
		h, m, _ := mskTime.Clock()
		acc = &tradeAcc{
			date:           mskDate,
			startedPre1530: h < 15 || (h == 15 && m < 30),
		}
		e.tradeAccs[sym] = acc
	}

	// Freeze the settlement VWAP before adding this trade: a post-15:30 trade
	// must not leak into the 15:30 session snapshot.
	e.maybeFreezeSettl(sym, mskTime)

	// Every deal counts toward day volume (completeness vs VOLTODAY) and the
	// rolling display VWAP, but only 10:00–15:30 deals enter the funding-leg VWAP —
	// the backfill replays the whole day including the 07:00 morning session.
	acc.dayV += tick.Volume
	if inFundingWindow(mskTime) {
		acc.sumPV += tick.Price * tick.Volume
		acc.sumV += tick.Volume
	}
	e.tradeVWAPs[sym].Add(tick.Price, tick.Volume, tick.Timestamp)
}

// maybeFreezeSettl freezes today's settlement VWAP at 15:30 MSK, once per symbol
// per day. The trade-based accumulator is preferred (exact, and it survives a
// mid-day restart thanks to the backfill); the ΔVOLTODAY accumulator is the
// fallback and also wins when the trade feed went stale mid-session (its own
// coverage would then be truncated). KindSettlePrice ticks can override later.
// Must be called while holding e.mu.
func (e *Engine) maybeFreezeSettl(sym string, mskTime time.Time) {
	if e.settlVWAP[sym] != nil {
		return
	}
	h, m, _ := mskTime.Clock()
	if h < 15 || (h == 15 && m < 30) {
		return
	}
	mskDate := mskTime.Format("2006-01-02")

	var tradeV float64
	tradeOK := false
	if tacc := e.tradeAccs[sym]; tacc != nil && tacc.date == mskDate && tacc.startedPre1530 {
		tradeV, tradeOK = tacc.vwap()
	}
	var sessV float64
	sessOK := false
	if acc := e.sessionAccs[sym]; acc != nil && acc.date == mskDate && acc.startedPre1530 {
		sessV, sessOK = acc.vwap()
	}

	var v float64
	switch {
	case tradeOK && (e.tradeFeedFresh(sym) || !sessOK):
		v = tradeV
	case sessOK:
		v = sessV
	default:
		return
	}

	e.settlVWAP[sym] = ptr(v)
	e.settlDate = mskDate
	e.freezeOfficialRateAtSettl(sym)
	e.tryFireSettlSignal(mskDate)
}

// tradeFeedFresh reports whether the trade feed has captured essentially all of
// the session volume marketdata reports for sym — the signal that the exact
// trade VWAP is authoritative. It is volume-based, NOT time-based: an illiquid
// instrument (EURRUBF) can go 10–20 min between deals during normal trading, and
// judging freshness by the age of the last deal wrongly demoted it to the empty
// ΔVOLTODAY fallback, zeroing its VWAP. A genuine shortfall (dead/slow trades
// endpoint while the future keeps trading) still trips the fallback because our
// captured volume then lags the growing VOLTODAY. Must be called while holding e.mu.
func (e *Engine) tradeFeedFresh(sym string) bool {
	acc := e.tradeAccs[sym]
	if acc == nil || acc.dayV <= 0 {
		return false
	}
	// Ignore a leftover accumulator from a previous day (no trades ingested yet
	// today). Uses the last price tick's date — a tick timestamp, not wall clock.
	if lp, ok := e.lastPriceAt[sym]; ok && acc.date != lp.In(msk).Format("2006-01-02") {
		return false
	}
	volToday, ok := e.vwapLastVol[sym]
	if !ok || volToday <= 0 {
		// No marketdata volume to compare against yet — trust the trades we have.
		return true
	}
	// Completeness is judged on whole-day volume: VOLTODAY counts from 07:00,
	// while the funding accumulator (sumV) holds only the 10:00–15:30 window.
	return acc.dayV >= volToday*tradeFeedMinCompleteness
}

// displayVWAP returns the rolling 6-hour VWAP for display: exact trade-based
// when the trade feed is fresh and has data in the window, otherwise the
// ΔVOLTODAY approximation. Must be called while holding e.mu.
func (e *Engine) displayVWAP(sym string, now time.Time) (float64, bool) {
	if e.tradeFeedFresh(sym) {
		if v, ok := e.tradeVWAPs[sym].Value(now); ok {
			return v, true
		}
	}
	return e.vwaps[sym].Value(now)
}

// bestSessionVWAP returns the current session VWAP for sym, preferring the
// trade-based accumulator when the trade feed is fresh. Must be called while
// holding e.mu.
func (e *Engine) bestSessionVWAP(sym string) (float64, bool) {
	if e.tradeFeedFresh(sym) {
		if acc := e.tradeAccs[sym]; acc != nil {
			if v, ok := acc.vwap(); ok {
				return v, true
			}
		}
	}
	if acc := e.sessionAccs[sym]; acc != nil {
		return acc.vwap()
	}
	return 0, false
}

// SeedEffectiveRates задаёт официальные курсы ЦБ, ДЕЙСТВУЮЩИЕ на дату dateMSK
// ("2006-01-02"). Вызывается один раз при старте сервиса (из журнала публикаций БД):
// после рестарта, случившегося уже ПОСЛЕ публикации ЦБ, движок сам не может узнать
// вчерашний курс — officialRate уже содержит завтрашний, — а границы формулы
// фандинга MOEX (K1/K2) масштабируются именно от действующего курса.
func (e *Engine) SeedEffectiveRates(dateMSK string, rates map[string]float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rateEffectiveDate = dateMSK
	for sym, rate := range rates {
		if rate > 0 {
			e.rateEffectiveToday[sym] = rate
		}
	}
}

// effectiveRate возвращает лучший известный курс ЦБ, ДЕЙСТВУЮЩИЙ сегодня
// (опубликованный вчера), — базу для границ K1/K2 формулы фандинга MOEX.
// Приоритет: курс, замороженный на 15:30 до публикации → текущий officialRate,
// пока сегодняшней публикации не было → засев из БД на сегодня. 0 = неизвестен.
// Must be called while holding e.mu.
func (e *Engine) effectiveRate(sym, officialSym string, now time.Time) float64 {
	if rate := e.officialRateAtSettl[sym]; rate > 0 {
		return rate
	}
	today := now.In(msk).Format("2006-01-02")
	if e.officialRateDate[officialSym] != today {
		if rate := e.officialRate[officialSym]; rate > 0 {
			return rate
		}
	}
	if e.rateEffectiveDate == today {
		return e.rateEffectiveToday[officialSym]
	}
	return 0
}

// freezeOfficialRateAtSettl сохраняет текущий курс ЦБ для sym на момент settlement.
// Вызывается только один раз при фиксации settlVWAP, чтобы прогнозный фандинг
// не менялся при последующей публикации ЦБ.
// После публикации ЦБ officialRate содержит курс на ЗАВТРА — замораживать его нельзя,
// иначе CBFunding будет вычислен с неверным (завтрашним) курсом. В этом случае пропускаем
// заморозку: CBFunding останется nil, что корректнее ложного значения.
func (e *Engine) freezeOfficialRateAtSettl(sym string) {
	offSym, ok := futuresOfficialSym[sym]
	if !ok {
		return
	}
	// Skip if CBR has already published today's rates — officialRate is then tomorrow's rate.
	today := time.Now().In(msk).Format("2006-01-02")
	if e.officialRateDate[offSym] == today {
		return
	}
	if rate, ok := e.officialRate[offSym]; ok {
		e.officialRateAtSettl[sym] = rate
	}
}

// freezeEURUSDFixing держит EUR/USD «по состоянию на 15:30 МСК» — курс ЕЦБ, который ЦБ РФ
// с 08.06.2026 использует для расчёта официального курса: EUR/RUB = USD/RUB(ЦБ) × EUR/USD@15:30.
// Значение обновляется каждым тиком EUR/USD до 15:30 МСК; в 15:30 обновления прекращаются, поэтому
// поле фиксирует курс ровно на этот момент (последний тик перед 15:30). Сбрасывается при смене
// торгового дня МСК. До 15:30 поле равно последнему живому курсу — прогноз EUR сходится к
// фактическому фиксингу ЦБ к моменту клиринга.
func (e *Engine) freezeEURUSDFixing(tick source.Tick) {
	mskTime := tick.Timestamp.In(msk)
	mskDate := mskTime.Format("2006-01-02")
	if e.eurUSDFixDate != mskDate {
		e.eurUSDFixDate = mskDate
		e.eurUSDFix = 0
	}
	h, m, _ := mskTime.Clock()
	if h < 15 || (h == 15 && m < 30) {
		e.eurUSDFix = tick.Price
	}
}

// Snapshot computes and returns current funding values for all instruments.
func (e *Engine) Snapshot() FundingSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()

	// Spot USD/RUB via CNY cross: USDCNH × CNY/RUB (using CNYRUBF last price as proxy).
	// EUR/RUB spot: EURUSD × USDCNH × CNY/RUB.
	// Zero values mean the respective rate has not been ingested yet.
	usdcnh := e.forexRates[source.SymbolUSDCNH]
	eurusd := e.forexRates[source.SymbolEURUSD]
	cnyRub := e.lastPrice[source.SymbolCNYRUBF]
	usdRubSpot := usdcnh * cnyRub
	eurRubSpot := eurusd * usdcnh * cnyRub

	// Predicted CB rates. USD: VWAP спот TOM за 10:00–15:30 МСК (методика ЦБ для USD/RUB).
	// EUR: с 08.06.2026 ЦБ считает курс как USD/RUB(ЦБ) × EUR/USD(ЕЦБ) по состоянию на 15:30 МСК,
	// поэтому наш прогноз — это произведение тех же ног: usdPredictedCBRate × EUR/USD@15:30.
	// EUR/RUB_TOM на бирже не торгуется, отдельной USD-ноги для EUR нет — используется общая USD.
	// Prefer the frozen 10:00–15:30 fixing predictor; fall back to the latest live
	// WAPRICE so the predicted row is populated even when the service started after 15:30.
	usdPredictedCBRate := e.spotTOMWAP[source.SymbolUSDRubTOM]
	if usdPredictedCBRate == 0 {
		usdPredictedCBRate = e.spotTOMWAPLive[source.SymbolUSDRubTOM]
	}
	// EUR/USD, зафиксированный на 15:30 МСК (см. freezeEURUSDFixing). До 15:30 равен живому курсу.
	eurUSD := e.eurUSDFix
	if eurUSD == 0 {
		// Фиксинг ещё не накоплен сегодня (старт сервиса после 15:30 или нет тиков) — живой курс.
		eurUSD = eurusd
	}
	if eurUSD == 0 {
		// Форекс-фид недоступен вовсе — оцениваем EUR/USD из отношения официальных курсов ЦБ.
		usdCBR := e.officialRate[source.SymbolUSDRubOfficial]
		eurCBR := e.officialRate[source.SymbolEURRubOfficial]
		if usdCBR > 0 && eurCBR > 0 {
			eurUSD = eurCBR / usdCBR
		}
	}
	eurPredictedCBRate := 0.0
	if eurUSD > 0 && usdPredictedCBRate > 0 {
		eurPredictedCBRate = eurUSD * usdPredictedCBRate
	}

	return FundingSnapshot{
		Timestamp:    now,
		USDRUBF:      e.buildFunding(source.SymbolUSDRUBF, source.SymbolUSDRubOfficial, usdRubSpot, usdPredictedCBRate, now),
		EURRUBF:      e.buildFunding(source.SymbolEURRUBF, source.SymbolEURRubOfficial, eurRubSpot, eurPredictedCBRate, now),
		CNYRUBF:      e.buildCNYFunding(now),
		USDTRUBPrice: e.lastPrice[source.SymbolUSDTRUB],
	}
}

// buildFunding produces InstrumentFunding for USD/RUB and EUR/RUB futures.
// spotRate and predictedCBRate are pre-computed by the caller; zero means unavailable.
// predictedCBRate for USD comes from USDRUB_TOM WAPRICE; for EUR via EUR/USD × USD cross.
func (e *Engine) buildFunding(sym, officialSym string, spotRate, predictedCBRate float64, now time.Time) InstrumentFunding {
	// Rolling VWAP (6-hour window) for live display and ForexFunding:
	// exact trade-based feed preferred, ΔVOLTODAY approximation as fallback.
	vwap, hasVWAP := e.displayVWAP(sym, now)
	last := e.lastPrice[sym]

	inf := InstrumentFunding{
		VWAP:      vwap,
		LastPrice: last,
	}

	if predictedCBRate > 0 {
		inf.PredictedCBRate = ptr(predictedCBRate)
	}

	// MOEXFunding: official swap_rate published by MOEX ISS every minute.
	if rate, ok := e.swapRate[sym]; ok {
		inf.MOEXFunding = ptr(rate)
	}

	// ForexFunding: raw deviation via CNY cross (market-based estimate).
	if spotRate > 0 && hasVWAP {
		inf.ForexFunding = ptr(vwap - spotRate)
	}

	// cbPublishedToday: ЦБ опубликовал новый курс именно сегодня (МСК).
	// Сравниваем с today, а не просто != "" — иначе вчерашняя дата даёт ложное срабатывание.
	today := now.In(msk).Format("2006-01-02")
	cbPublishedToday := e.officialRateDate[officialSym] == today

	settlPtr := e.settlVWAP[sym]
	settlDone := settlPtr != nil

	// CBFunding — НАШ расчёт от официального курса ЦБ, появляется ТОЛЬКО после его
	// публикации (до этого строка пустая — семантика поля):
	//   D = clamp(settleVWAP(15:30) − курс ЦБ, установленный сегодня)
	// SWAPRATE сюда не подмешивается никогда: что начисляет MOEX, показывает отдельное
	// поле MOEXFunding. Раздельные источники позволяют сверять наш расчёт с биржевым
	// (14.07: реконструкция −0.1162 против официального SWAPRATE −0.11493).
	if settlDone && cbPublishedToday {
		if newRate, ok := e.officialRate[officialSym]; ok && newRate > 0 {
			// Отклонение d — от НОВОГО курса (зафиксирован сегодня, действует завтра),
			// но границы K1/K2 MOEX масштабирует от курса, ДЕЙСТВУЮЩЕГО сегодня.
			// Сверено с фактом 14.07: SWAPRATE = −0.11493 = −0.0015 × 76.6213
			// (вчерашний курс), а границы от нового 77.4912 давали бы −0.11624.
			base := e.effectiveRate(sym, officialSym, now)
			if base <= 0 {
				base = newRate // курс на сегодня неизвестен (нет засева) — деградация к новому
			}
			d := *settlPtr - newRate
			l1 := 0.001 * base
			l2 := 0.0015 * base
			inf.CBFunding = ptr(clampFunding(d, l1, l2))
		}
	}

	// OfficialRate is the most recent CBR publication, shown in the UI for reference.
	if rate, ok := e.officialRate[officialSym]; ok {
		inf.OfficialRate = ptr(rate)
	}

	// PredictedFunding: the funding MOEX will charge at clearing, estimated live before the
	// CBR publishes. Both legs accumulate over the same 10:00–15:30 MSK window, so by 15:30
	// the prediction converges to the actual CBFunding:
	//   d = sessionVWAP(futures) − predictedCBRate(spot TOM VWAP)
	// Deadband/cap are scaled by the predicted rate (K1=0.1%, K2=0.15%), matching the MOEX formula.
	// After 15:30 the futures leg is FROZEN at the settlement VWAP: the live session
	// accumulator keeps ingesting post-settlement trades, while the CB leg froze at 15:30 —
	// mixing them made the prediction drift away from the value funding is actually fixed on.
	// Hidden once CBFunding takes over (settlement done + CBR published today).
	if inf.PredictedCBRate != nil && !(settlDone && cbPublishedToday) {
		var futVWAP float64
		hasFut := false
		if settlDone {
			futVWAP, hasFut = *settlPtr, true
		} else {
			futVWAP, hasFut = e.bestSessionVWAP(sym)
		}
		if hasFut {
			predRate := *inf.PredictedCBRate
			// Границы — от действующего сегодня курса ЦБ (как в самой формуле MOEX);
			// прогнозный курс — лишь фолбэк, пока действующий неизвестен.
			base := e.effectiveRate(sym, officialSym, now)
			if base <= 0 {
				base = predRate
			}
			d := futVWAP - predRate
			l1 := 0.001 * base
			l2 := 0.0015 * base
			inf.PredictedFunding = ptr(clampFunding(d, l1, l2))
		}
	}

	return inf
}

// buildCNYFunding produces InstrumentFunding for CNY/RUB futures.
// MOEXFunding comes from MOEX ISS swap_rate; no ForexFunding or CBFunding for CNYRUBF.
func (e *Engine) buildCNYFunding(now time.Time) InstrumentFunding {
	vwap, _ := e.displayVWAP(source.SymbolCNYRUBF, now)
	last := e.lastPrice[source.SymbolCNYRUBF]

	inf := InstrumentFunding{
		VWAP:      vwap,
		LastPrice: last,
	}
	if rate, ok := e.swapRate[source.SymbolCNYRUBF]; ok {
		inf.MOEXFunding = ptr(rate)
	}
	return inf
}

func ptr(f float64) *float64 { return &f }

// clampFunding applies the MOEX funding formula:
// Funding = MIN(l2, MAX(-l2, MIN(-l1, d) + MAX(l1, d)))
// d = raw deviation (futures - spot); l1 = K1*spot (deadband); l2 = K2*spot (cap).
func clampFunding(d, l1, l2 float64) float64 {
	inner := math.Min(-l1, d) + math.Max(l1, d)
	return math.Min(l2, math.Max(-l2, inner))
}
