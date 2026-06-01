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

// Engine ingests Ticks from any source and computes FundingSnapshots on demand.
// All fields are protected by mu; VWAPCalculators have their own internal mutexes.
type Engine struct {
	vwaps        map[string]*VWAPCalculator // 6-hour rolling VWAP for display
	sessionAccs  map[string]*sessionAcc     // cumulative session VWAP (reset at MSK midnight)
	spotTOMWAP   map[string]float64         // WAPRICE from MOEX ISS for spot TOM → predicted CBR rate (frozen after 15:30)
	settlVWAP        map[string]*float64        // sentinel: non-nil once settlement has occurred
	settlDate        string                     // MSK date for which settlement was recorded
	lastPrice        map[string]float64
	swapRate         map[string]float64
	forexRates       map[string]float64
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
	for _, sym := range futures {
		vwaps[sym] = NewVWAP(6 * time.Hour)
	}
	return &Engine{
		vwaps:            vwaps,
		sessionAccs:      make(map[string]*sessionAcc),
		spotTOMWAP:       make(map[string]float64),
		settlVWAP:        make(map[string]*float64),
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
			e.vwaps[tick.Symbol].Add(tick.Price, tick.Volume, tick.Timestamp)
			e.lastPrice[tick.Symbol] = tick.Price
			e.ingestSessionTick(tick)
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
		// CBR official-rate methodology. Only store it inside the CBR window so the value
		// freezes at the closing VWAP once trading beyond 15:30 would otherwise skew it.
		if tick.Kind == source.KindWaprice && inCBRWindow(tick.Timestamp) {
			e.spotTOMWAP[tick.Symbol] = tick.Price
		}

	case source.SymbolEURUSD, source.SymbolUSDCNH:
		e.forexRates[tick.Symbol] = tick.Price

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
	// Only valid when the accumulator was started before 15:30 (not a mid-day restart).
	// KindSettlePrice ticks from MOEX ISS can override this at any time.
	if e.settlVWAP[sym] == nil && acc.startedPre1530 {
		h, m, _ := mskTime.Clock()
		if h > 15 || (h == 15 && m >= 30) {
			if v, ok := acc.vwap(); ok {
				e.settlVWAP[sym] = ptr(v)
				e.settlDate = mskDate
				e.freezeOfficialRateAtSettl(sym)
				e.tryFireSettlSignal(mskDate)
			}
		}
	}
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

	// Predicted CB rates: USD from spot TOM WAPRICE; EUR via cross-rate (EUR/RUB_TOM doesn't trade).
	// EUR/USD proxy: CBR official rates ratio (EUR/RUB ÷ USD/RUB) — available without a forex API.
	// Falls back to forexRates EUR/USD if available (e.g. TwelveData feed is wired up).
	usdPredictedCBRate := e.spotTOMWAP[source.SymbolUSDRubTOM]
	eurUSD := eurusd // from forex source (may be zero if no feed is configured)
	if eurUSD == 0 {
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
	// Rolling VWAP (6-hour window) for live display and PredictedFunding.
	vwap, hasVWAP := e.vwaps[sym].Value(now)
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

	// CBFunding: доступен только когда состоялись И клиринг (15:30), И публикация ЦБ сегодня.
	// Используем officialRate (курс, опубликованный ЦБ в данный торговый день и вступающий
	// в силу на следующий календарный день) — именно это значение фигурирует в формуле MOEX:
	//   D = VWAP(10:00–15:30) − курс ЦБ, установленный сегодня.
	if settlDone && cbPublishedToday {
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
		if acc := e.sessionAccs[sym]; acc != nil {
			if sessVWAP, ok := acc.vwap(); ok {
				predRate := *inf.PredictedCBRate
				d := sessVWAP - predRate
				l1 := 0.001 * predRate
				l2 := 0.0015 * predRate
				inf.PredictedFunding = ptr(clampFunding(d, l1, l2))
			}
		}
	}

	return inf
}

// buildCNYFunding produces InstrumentFunding for CNY/RUB futures.
// MOEXFunding comes from MOEX ISS swap_rate; no ForexFunding or CBFunding for CNYRUBF.
func (e *Engine) buildCNYFunding(now time.Time) InstrumentFunding {
	vwap, _ := e.vwaps[source.SymbolCNYRUBF].Value(now)
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
