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


// inCBRWindow reports whether t falls inside the CBR official-rate methodology window
// (10:00–15:30 MSK). WAPRICE ticks outside this window are discarded so the stored
// value freezes at the closing VWAP, which equals the actual CBR fixing.
func inCBRWindow(t time.Time) bool {
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

// sessionAcc accumulates a cumulative volume-weighted sum for the trading session.
// Volumes from MOEX ISS are VOLTODAY (running total); we track deltas to get
// proper incremental weights. The accumulator resets when the MSK date changes.
type sessionAcc struct {
	sumPV          float64 // Σ(price × ΔvolToday)
	sumV           float64 // Σ(ΔvolToday)
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

// tradeAcc accumulates the session VWAP from individual executed deals
// (KindTrade ticks). Unlike sessionAcc it needs no delta arithmetic: each
// tick already carries the volume of exactly one trade.
type tradeAcc struct {
	sumPV          float64 // Σ(price × quantity)
	sumV           float64 // Σ(quantity)
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
			// MOEX ISS returns SETTLEPRICE all day — before 15:30 it's yesterday's value.
			// Only accept it after 15:30 MSK when it reflects the current day's settlement.
			// Freeze once: after a service restart MOEX returns the current price as SETTLEPRICE,
			// not the official 15:30 clearing price, so we must not overwrite an already-set value.
			mskTime := tick.Timestamp.In(msk)
			h, m, _ := mskTime.Clock()
			if h > 15 || (h == 15 && m >= 30) {
				if e.settlVWAP[tick.Symbol] == nil {
					e.settlVWAP[tick.Symbol] = ptr(tick.Price)
					e.freezeOfficialRateAtSettl(tick.Symbol)
					e.tryFireSettlSignal(mskTime.Format("2006-01-02"))
				}
			}
		}

	case source.SymbolUSDRubTOM:
		// WAPRICE is the MOEX ISS session VWAP from market open (10:00 MSK), matching the
		// CBR official-rate methodology. The 10:00–15:30 value (spotTOMWAP) is the best
		// fixing predictor — frozen at the window close so post-15:30 trades don't skew it.
		// We also keep the latest WAPRICE (spotTOMWAPLive) regardless of time so the
		// predicted row falls back to a live value instead of being empty on a late start.
		if tick.Kind == source.KindWaprice {
			e.spotTOMWAPLive[tick.Symbol] = tick.Price
			if inCBRWindow(tick.Timestamp) {
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
		// Bootstrap accumulator with the first tick's full VOLTODAY weight.
		h0, m0, _ := mskTime.Clock()
		e.sessionAccs[sym] = &sessionAcc{
			sumPV:          tick.Price * tick.Volume,
			sumV:           tick.Volume,
			lastVol:        tick.Volume,
			date:           mskDate,
			startedPre1530: h0 < 15 || (h0 == 15 && m0 < 30),
		}
		acc = e.sessionAccs[sym]
	} else {
		deltaVol := tick.Volume - acc.lastVol
		if deltaVol > 0 {
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

	acc.sumPV += tick.Price * tick.Volume
	acc.sumV += tick.Volume
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
	if acc == nil || acc.sumV <= 0 {
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
	return acc.sumV >= volToday*tradeFeedMinCompleteness
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

	// CBFunding: предпочитаем авторитетный SWAPRATE, который MOEX публикует на вечернем
	// клиринге — это и есть фандинг, который MOEX реально начисляет (равен тому, что
	// показывает эталонный терминал). Пока SWAPRATE не опубликован (= 0), используем нашу
	// раннюю реконструкцию из свежего курса ЦБ как опережающую оценку:
	//   D = VWAP(10:00–15:30) − курс ЦБ, установленный сегодня.
	if rate, ok := e.swapRate[sym]; ok && rate != 0 {
		inf.CBFunding = ptr(rate)
	} else if settlDone && cbPublishedToday {
		if newRate, ok := e.officialRate[officialSym]; ok && newRate > 0 {
			d := *settlPtr - newRate
			l1 := 0.001 * newRate
			l2 := 0.0015 * newRate
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
	// Hidden once CBFunding takes over (settlement done + CBR published today).
	if inf.PredictedCBRate != nil && !(settlDone && cbPublishedToday) {
		if sessVWAP, ok := e.bestSessionVWAP(sym); ok {
			predRate := *inf.PredictedCBRate
			d := sessVWAP - predRate
			l1 := 0.001 * predRate
			l2 := 0.0015 * predRate
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
