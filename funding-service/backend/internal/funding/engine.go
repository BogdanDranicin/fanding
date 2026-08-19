package funding

import (
	"math"
	"sync"
	"time"

	"github.com/rs/zerolog"

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

// atOrAfterSettl reports whether t is at or past the 15:30 MSK window close.
func atOrAfterSettl(t time.Time) bool {
	h, m, _ := t.In(msk).Clock()
	return h > 15 || (h == 15 && m >= 30)
}

// afterSettlGrace — истёк ли крайний срок ожидания, до которого движок держит ногу
// фьючерса незамороженной, пока поток сделок не перешагнул 15:30 своим собственным
// временем. После него замораживаем тем, что есть: фандинг всё равно нужен только
// к публикации курса ЦБ (после 17:30 МСК), запас огромный.
//
// Срок 17:00, а не 15:45 (как было до 06.08.2026). Отсрочка в 15 минут совпадала с
// типичным отставанием фида, и поток сделок регулярно не успевал довезти хвост окна:
// заморозка происходила по этой ветке, а не по реально пришедшей сделке ≥15:30.
// Проверено на 06.08.2026: наша нога USDRUBF = 81.51676 против эталонной 81.51729 по
// сырым сделкам ISS — ровно окно, обрезанное на 15:25 (потеряно 2520 из 244789
// контрактов). В фандинге это дало −0.00053 против биржевого SWAPRATE. Полнота по
// объёму (tradeFeedMinCompleteness) такую потерю не ловит: 5 минут — это ~1% дня.
func afterSettlGrace(t time.Time) bool {
	h, _, _ := t.In(msk).Clock()
	return h >= 17
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
	MOEXFunding      *float64 // swap_rate from MOEX ISS; nil until first ISS poll returns SWAPRATE
	CBFunding        *float64 // clamp(settle_price − CBR_rate, K1, K2); non-nil once both settlement and CBR rate are available
	OfficialRate     *float64 // most recent CBR rate; nil until published
	PredictedFunding *float64 // live clamp(sessionVWAP_futures − predictedCBRate, K1·rate, K2·rate)
	PredictedCBRate  *float64 // live estimate of today's CBR fixing: VWAP of spot TOM over 10:00–15:30 MSK

	// Диагностика реконструкции CBFunding (для журнала сверки с биржей):
	SettlVWAP           *float64 // нога фьючерса на 15:30 (settlVWAP), non-nil после клиринга
	// SettlProvisional — нога заморожена аварийно (поток сделок не дошёл до 15:30),
	// окно могло быть обрезано, и CBFunding по ней ещё уточнится. Уведомление о
	// публикации по такому значению не рассылается без пометки.
	SettlProvisional    bool
	CBFundingNoDeadband *float64 // CBFunding БЕЗ мёртвой зоны K1 — clamp(d, ±l2); чтобы видеть, зануляет ли K1
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
	// sawPost1530 — фид сделок сам перешагнул 15:30: пришла сделка со временем
	// ≥15:30. Только это доказывает, что окно 10:00–15:30 довезено полностью.
	// Данные ISS запаздывают ~15 минут, поэтому «настенные» 15:30 ничего не значат.
	sawPost1530 bool
	// lastTradeAt — биржевое время самой свежей учтённой сделки. Нужно только для
	// диагностики: по нему в логе аварийной заморозки видно, где именно встал поток
	// сделок, — иначе обрезанное окно приходится вычислять задним числом по сырой
	// ленте ISS, а она живёт лишь до конца торгового дня.
	lastTradeAt time.Time
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
	spotTOMWAPDate map[string]string        // MSK date the frozen spotTOMWAP belongs to (суточный сброс)
	spotTOMWAPLive map[string]float64       // latest WAPRICE for spot TOM (any time) → fallback so the predicted row is never empty on a late start
	settlVWAP        map[string]*float64        // sentinel: non-nil once settlement has occurred
	// settlProvisional — нога заморожена аварийно, по истёкшей отсрочке, а не по
	// сделке, перешагнувшей 15:30. Окно у такой ноги могло не доехать до конца, и
	// её обязаны переписать, как только поток сделок сам перейдёт границу.
	settlProvisional map[string]bool
	settlDate        string                     // MSK date for which settlement was recorded
	vwapLastVol      map[string]float64         // last VOLTODAY per symbol, to weight the rolling VWAP by ΔVOLTODAY
	lastPrice        map[string]float64
	swapRate         map[string]float64
	prevSettle          map[string]float64 // PREVSETTLEPRICE ISS: расчётная цена предыдущего вечернего клиринга
	prevSettleAtSettl   map[string]float64 // та же цена, замороженная на 15:30 (вечерний клиринг её перезапишет)
	officialRate        map[string]float64
	officialRateDate    map[string]string  // MSK date when officialRate was last published
	officialRateAtSettl map[string]float64 // курс ЦБ, зафиксированный при settlement (15:30)
	rateEffectiveToday  map[string]float64 // засев из БД: курс ЦБ, ДЕЙСТВУЮЩИЙ сегодня (опубликован вчера); officialSym -> rate
	rateEffectiveDate   string             // MSK-дата, на которую действителен rateEffectiveToday
	cbLoggedDate        map[string]string  // sym -> MSK-дата, за которую уже напечатана диагностика CBFunding (раз в сутки)
	log                 zerolog.Logger
	mu                  sync.Mutex
}

// NewEngine creates an Engine with a 6-hour rolling VWAP window.
//
// Заморозка ноги фьючерса (~15:30) наружу больше не сигналит: единственным
// потребителем был Telegram-диспетчер, славший служебное «Сервис перезапущен».
// Сообщение убрано (06.08.2026) — клиринг сам по себе не новость для подписчика,
// а вердикт по фандингу приходит отдельным сообщением после публикации курса ЦБ.
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
		spotTOMWAPDate:   make(map[string]string),
		spotTOMWAPLive:   make(map[string]float64),
		settlVWAP:        make(map[string]*float64),
		settlProvisional: make(map[string]bool),
		vwapLastVol:      make(map[string]float64),
		lastPrice:        make(map[string]float64),
		swapRate:         make(map[string]float64),
		prevSettle:          make(map[string]float64),
		prevSettleAtSettl:   make(map[string]float64),
		officialRate:        make(map[string]float64),
		officialRateDate:    make(map[string]string),
		officialRateAtSettl: make(map[string]float64),
		rateEffectiveToday:  make(map[string]float64),
		cbLoggedDate:        make(map[string]string),
		log:                 zerolog.Nop(),
	}
}

// SetLogger attaches a logger for the once-per-day CBFunding diagnostic. Without
// it (tests) the engine logs to a no-op writer.
func (e *Engine) SetLogger(l zerolog.Logger) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = l
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
		case source.KindPrevSettle:
			e.prevSettle[tick.Symbol] = tick.Price
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
			// Суточный сброс замороженного окна 10:00–15:30: значение действительно только
			// за свой торговый день. Без сброса вчерашний фиксинг переживал бы полночь и
			// предпочитался бы живому WAPRICE (spotTOMWAP выигрывает у spotTOMWAPLive в
			// Snapshot), давая ложную «ошибку прогноза» на новом дне до наполнения окна —
			// именно это ломало предсказанный курс.
			mskDate := tick.Timestamp.In(msk).Format("2006-01-02")
			if e.spotTOMWAPDate[tick.Symbol] != mskDate {
				e.spotTOMWAPDate[tick.Symbol] = mskDate
				delete(e.spotTOMWAP, tick.Symbol)
			}
			if inFundingWindow(tick.Timestamp) {
				e.spotTOMWAP[tick.Symbol] = tick.Price
			}
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
			delete(e.settlProvisional, sym)
			delete(e.officialRateAtSettl, sym)
			delete(e.prevSettleAtSettl, sym)
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

	// Сделка со временем ≥15:30 — сигнал, что поток сделок довёз окно до конца.
	// Ставим флаг ДО заморозки: именно эта сделка её и разрешает, а в окно она
	// не попадёт (inFundingWindow ниже). Сделки чужих дней помечены временем
	// утреннего клиринга — доказательством перехода границы они быть не могут.
	if !tick.Backdated && atOrAfterSettl(mskTime) {
		acc.sawPost1530 = true
	}

	// Freeze the settlement VWAP before adding this trade: a post-15:30 trade
	// must not leak into the 15:30 session snapshot.
	e.maybeFreezeSettl(sym, mskTime)

	// Every deal counts toward day volume (completeness vs VOLTODAY) and the
	// rolling display VWAP, but only 10:00–15:30 deals enter the funding-leg VWAP —
	// the backfill replays the whole day including the 07:00 morning session.
	acc.dayV += tick.Volume

	// Сделка чужого дня, приписанная биржей к сегодняшней сессии (выходные, вечёрка
	// прошлого дня): её объём VOLTODAY считает — поэтому dayV выше уже прибавлен, —
	// но цена относится к другому дню и в средневзвешенную не входит. Биржа считает
	// так же: 27.07.2026 её минутные свечи покрывали ровно объём БЕЗ таких сделок,
	// а с ними наш VWAP окна уезжал на 0.0144 вниз (78.10417 против биржевых 78.11873).
	if tick.Backdated {
		return
	}

	if mskTime.After(acc.lastTradeAt) {
		acc.lastTradeAt = mskTime
	}

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
//
// mskTime — РЫНОЧНОЕ время тика (момент, к которому относятся данные), а не время
// ответа сервера ISS: публичный фид запаздывает ~15 минут (см. moexiss.parseTime).
// Must be called while holding e.mu.
func (e *Engine) maybeFreezeSettl(sym string, mskTime time.Time) {
	// Замороженную ногу переписывать нельзя — кроме одного случая: она заморожена
	// аварийно, по отсрочке, и окно могло быть обрезано. Тогда ждём момента, когда
	// поток сделок сам перешагнёт 15:30, и заменяем значение точным.
	frozen := e.settlVWAP[sym] != nil
	if frozen && !e.settlProvisional[sym] {
		return
	}
	h, m, _ := mskTime.Clock()
	if h < 15 || (h == 15 && m < 30) {
		return
	}
	mskDate := mskTime.Format("2006-01-02")

	var tradeV float64
	var tradeFeedAt time.Time
	tradeOK, tradeCrossed := false, false
	if tacc := e.tradeAccs[sym]; tacc != nil && tacc.date == mskDate && tacc.startedPre1530 {
		tradeV, tradeOK = tacc.vwap()
		tradeCrossed = tacc.sawPost1530
		tradeFeedAt = tacc.lastTradeAt
	}
	var sessV float64
	sessOK := false
	if acc := e.sessionAccs[sym]; acc != nil && acc.date == mskDate && acc.startedPre1530 {
		sessV, sessOK = acc.vwap()
	}

	// Ключевое условие: замораживать ногу фьючерса можно, только когда САМ поток
	// сделок дошёл до 15:30. Публичный фид ISS запаздывает ~15 минут, и тик
	// marketdata с рыночным временем 15:30 приходит на 15 минут раньше последних
	// сделок окна. Раньше движок морозил по нему — и получал окно, обрезанное на
	// ~15:15 (28.07.2026: USD −0.00225, EUR +0.01305 против биржи).
	// Исключение — истёкшая отсрочка: если сделок так и нет (мёртвый эндпоинт) или
	// поток застрял, работаем тем, что есть, лишь бы не остаться без фандинга.
	graceOver := afterSettlGrace(mskTime)
	tradeReady := tradeOK && (tradeCrossed || (graceOver && e.tradeFeedFresh(sym)))

	// Уточнение аварийно замороженной ноги. Ждём именно сделки за 15:30: сделки
	// приходят строго по возрастанию TRADENO, поэтому её появление доказывает, что
	// всё окно уже у нас (частичный ответ ленты не создаёт дыр — курсор остаётся на
	// последней разобранной сделке, и следующий опрос продолжает с неё).
	//
	// 19.08.2026 без этого EUR-фандинг ушёл подписчикам как 0.03367 вместо 0.02919:
	// нога замёрзла на окне до 15:11, а точное значение движок получил на пять минут
	// позже — и переписал его только по случайности, из-за суточного сброса.
	if frozen {
		if !tradeOK || !tradeCrossed {
			return
		}
		old := *e.settlVWAP[sym]
		e.settlVWAP[sym] = ptr(tradeV)
		delete(e.settlProvisional, sym)
		e.log.Warn().
			Str("sym", sym).
			Str("date", mskDate).
			Float64("settl_vwap_was", old).
			Float64("settl_vwap", tradeV).
			Float64("delta", tradeV-old).
			Msg("нога фьючерса уточнена: поток сделок довёз окно до 15:30")
		return
	}

	var v float64
	switch {
	case tradeReady && (e.tradeFeedFresh(sym) || !sessOK):
		v = tradeV
	case sessOK && (tradeReady || graceOver || !tradeOK):
		v = sessV
	default:
		// Поток сделок ещё догоняет окно — ждём следующего тика.
		return
	}

	// Заморозка по отсрочке — аварийная: поток сделок так и не перешагнул 15:30, и
	// хвост окна мог не доехать (именно так набегало расхождение с биржей). Пишем в
	// лог, чтобы такие дни было видно при сверке, а не искать их снова по фандингу.
	if !tradeCrossed {
		e.settlProvisional[sym] = true
		e.log.Warn().
			Str("sym", sym).
			Str("date", mskDate).
			Float64("settl_vwap", v).
			Bool("trade_vwap_used", tradeReady && (e.tradeFeedFresh(sym) || !sessOK)).
			Str("trade_feed_at", tradeFeedAt.Format("15:04:05")).
			Msg("нога фьючерса заморожена по отсрочке: поток сделок не дошёл до 15:30, окно может быть обрезано")
	}

	e.settlVWAP[sym] = ptr(v)
	e.settlDate = mskDate
	e.freezeOfficialRateAtSettl(sym)
	// Расчётную цену предыдущего клиринга тоже фиксируем: вечерний клиринг (19:00)
	// перезапишет PREVSETTLEPRICE сегодняшней ценой, а границы K1/K2 сегодняшнего
	// фандинга масштабируются от вчерашней — иначе значение поехало бы вечером.
	if base, ok := e.prevSettle[sym]; ok && base > 0 {
		e.prevSettleAtSettl[sym] = base
	}
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

// fundingBase возвращает «ЦенаСпот» формулы MOEX — базу границ L1=K1·base и
// L2=K2·base. Биржа берёт РАСЧЁТНУЮ ЦЕНУ предыдущего вечернего клиринга
// (PREVSETTLEPRICE у ISS); вечерний клиринг ставит её равной опубликованному в тот
// день курсу ЦБ, округлённому до шага цены (0.01), поэтому «вчерашний курс ЦБ» —
// корректный, но чуть менее точный фолбэк. Сверено с фактом:
//   14.07.2026 кап USDRUBF = −0.11493 = −0.0015 × 76.62 (PREVSETTLE), тик-в-тик;
//   27.07.2026 кап EURRUBF =  0.13334 =  0.0015 × 88.89 (PREVSETTLE), тик-в-тик,
//     тогда как от нового курса 88.7602 получалось 0.13314 — это и был наш зазор.
// Приоритет: цена, замороженная на 15:30 → живая PREVSETTLEPRICE → действующий курс
// ЦБ. 0 = база неизвестна. Must be called while holding e.mu.
func (e *Engine) fundingBase(sym, officialSym string, now time.Time) float64 {
	if base := e.prevSettleAtSettl[sym]; base > 0 {
		return base
	}
	if base := e.prevSettle[sym]; base > 0 {
		return base
	}
	return e.effectiveRate(sym, officialSym, now)
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
	// EUR/USD оцениваем из отношения последних официальных курсов ЦБ: они посчитаны
	// самим ЦБ по фиксингу ЕЦБ на 15:30, то есть по той же ноге, что нужна прогнозу.
	// Прямого форекс-фида у сервиса нет (источник TwelveData убран 06.08.2026 вместе
	// с Forex funding — на бесплатном тарифе он всё равно не был подключён в проде).
	eurUSD := 0.0
	usdCBR := e.officialRate[source.SymbolUSDRubOfficial]
	eurCBR := e.officialRate[source.SymbolEURRubOfficial]
	if usdCBR > 0 && eurCBR > 0 {
		eurUSD = eurCBR / usdCBR
	}
	eurPredictedCBRate := 0.0
	if eurUSD > 0 && usdPredictedCBRate > 0 {
		eurPredictedCBRate = eurUSD * usdPredictedCBRate
	}

	return FundingSnapshot{
		Timestamp:    now,
		USDRUBF:      e.buildFunding(source.SymbolUSDRUBF, source.SymbolUSDRubOfficial, usdPredictedCBRate, now),
		EURRUBF:      e.buildFunding(source.SymbolEURRUBF, source.SymbolEURRubOfficial, eurPredictedCBRate, now),
		CNYRUBF:      e.buildCNYFunding(now),
		USDTRUBPrice: e.lastPrice[source.SymbolUSDTRUB],
	}
}

// buildFunding produces InstrumentFunding for USD/RUB and EUR/RUB futures.
// predictedCBRate is pre-computed by the caller; zero means unavailable.
// predictedCBRate for USD comes from USDRUB_TOM WAPRICE; for EUR via EUR/USD × USD cross.
func (e *Engine) buildFunding(sym, officialSym string, predictedCBRate float64, now time.Time) InstrumentFunding {
	// Rolling VWAP (6-hour window) for live display: exact trade-based feed
	// preferred, ΔVOLTODAY approximation as fallback.
	// После 15:30 показываем ЗАМОРОЖЕННУЮ ногу фьючерса (settlement VWAP): именно на
	// ней зафиксирован сегодняшний фандинг, и «живое» число, продолжающее ползти
	// вечером, только вводило в заблуждение при сверке с биржей.
	vwap, _ := e.displayVWAP(sym, now)
	if settl := e.settlVWAP[sym]; settl != nil {
		vwap = *settl
	}
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

	// cbPublishedToday: ЦБ опубликовал новый курс именно сегодня (МСК).
	// Сравниваем с today, а не просто != "" — иначе вчерашняя дата даёт ложное срабатывание.
	today := now.In(msk).Format("2006-01-02")
	cbPublishedToday := e.officialRateDate[officialSym] == today

	settlPtr := e.settlVWAP[sym]
	settlDone := settlPtr != nil
	if settlDone {
		inf.SettlVWAP = settlPtr // нога фьючерса на 15:30 — для журнала сверки
		inf.SettlProvisional = e.settlProvisional[sym]
	}

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
			base := e.fundingBase(sym, officialSym, now)
			if base <= 0 {
				base = newRate // база неизвестна (нет ни PREVSETTLE, ни засева) — деградация к новому
			}
			d := *settlPtr - newRate
			l1 := 0.001 * base
			l2 := 0.0015 * base
			cb := clampFunding(d, l1, l2)
			inf.CBFunding = ptr(cb)

			// cbNoDeadband — чем был бы CBFunding без мёртвой зоны K1 (только кап ±l2).
			// Кладём в снапшот, чтобы журнал показал: зануляет ли K1 реальный фандинг
			// (если факт SWAPRATE ближе к нему, чем к cb — значит K1 надо убирать).
			cbNoDeadband := math.Max(-l2, math.Min(l2, d))
			inf.CBFundingNoDeadband = ptr(cbNoDeadband)

			// Диагностика реконструкции CBFunding (раз в сутки на символ, на момент
			// публикации) в лог — дублирует журнальную строку для быстрой сверки.
			// Официальная методика MOEX (moex.com/a8141, USD/EUR c 24.06.2024):
			//   D = VWAP фьючерса 10:00–15:30 безадресных − официальный курс ЦБ за день,
			//   Funding = clamp(D, ±L2) c мёртвой зоной L1; L1=K1·курс, L2=K2·курс,
			//   K1=0.1%, K2=0.15%. settl_vwap и newRate — ровно эти две ноги.
			// SWAPRATE клиринга к моменту публикации ещё не вышел (moex_swaprate_at_pub=0);
			// фактическую сверку даёт вечерний poll журнала (moex_funding vs cb_funding).
			if e.cbLoggedDate[sym] != today {
				e.cbLoggedDate[sym] = today
				e.log.Warn().
					Str("sym", sym).
					Float64("settl_vwap", *settlPtr).
					Float64("cb_rate_new", newRate).
					Float64("base_effective_rate", base).
					Float64("d", d).
					Float64("l1_deadband", l1).
					Float64("l2_cap", l2).
					Float64("cb_funding", cb).
					Float64("cb_funding_no_deadband", cbNoDeadband).
					Float64("moex_swaprate_at_pub", e.swapRate[sym]).
					Str("date", today).
					Msg("CBFunding diag: реконструкция по методике MOEX для сверки с фактическим SWAPRATE")
			}
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
	// Прогноз живёт ТОЛЬКО до 15:30. После заморозки ноги фьючерса он больше ничего не
	// предсказывает — обе ноги неподвижны, а нога ЦБ у нас взята с почти не торгуемого
	// спота USDRUB_TOM и врёт (27.07.2026: прогноз −0.117 против факта +0.0235). Держать
	// на сайте застывшее неверное число хуже, чем пустую строку: до публикации курса ЦБ
	// точного ответа не существует, а сразу после неё появляется CBFunding.
	if inf.PredictedCBRate != nil && !settlDone {
		futVWAP, hasFut := e.bestSessionVWAP(sym)
		if hasFut {
			predRate := *inf.PredictedCBRate
			// Границы — от расчётной цены предыдущего клиринга (как в самой формуле
			// MOEX); прогнозный курс — лишь фолбэк, пока база неизвестна.
			base := e.fundingBase(sym, officialSym, now)
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
// MOEXFunding comes from MOEX ISS swap_rate; no CBFunding for CNYRUBF.
func (e *Engine) buildCNYFunding(now time.Time) InstrumentFunding {
	vwap, _ := e.displayVWAP(source.SymbolCNYRUBF, now)
	if settl := e.settlVWAP[source.SymbolCNYRUBF]; settl != nil {
		vwap = *settl // после 15:30 — как у USD/EUR: замороженная нога фьючерса
	}
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
