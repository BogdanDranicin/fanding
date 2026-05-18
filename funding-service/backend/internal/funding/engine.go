package funding

import (
	"sync"
	"time"

	"github.com/funding-service/backend/internal/source"
)

// FundingSnapshot holds the latest computed values for all tracked instruments.
type FundingSnapshot struct {
	Timestamp    time.Time
	USDRUBF      InstrumentFunding
	EURRUBF      InstrumentFunding
	CNYRUBF      InstrumentFunding
	USDTRUBPrice float64
}

// InstrumentFunding holds VWAP, last price, and the three funding types for one instrument.
// Pointer fields are nil until the required data arrives.
type InstrumentFunding struct {
	VWAP         float64
	LastPrice    float64
	ForexFunding *float64 // nil until at least one forex tick is ingested
	MOEXFunding  *float64 // VWAP - LastPrice; nil until both are non-zero
	CBFunding    *float64 // nil until CBR publishes the official rate
	OfficialRate *float64 // most recent CBR rate; nil until published
}

// Engine ingests Ticks from any source and computes FundingSnapshots on demand.
// All fields are protected by mu; VWAPCalculators have their own internal mutexes.
type Engine struct {
	vwaps        map[string]*VWAPCalculator // futures symbols only
	lastPrice    map[string]float64         // most recent price per symbol
	forexRates   map[string]float64         // EURUSD, USDCNH
	officialRate map[string]float64         // USD/RUB and EUR/RUB from CBR
	mu           sync.Mutex
}

// NewEngine creates an Engine with a 1-minute VWAP window.
func NewEngine() *Engine {
	futures := []string{source.SymbolUSDRUBF, source.SymbolEURRUBF, source.SymbolCNYRUBF}
	vwaps := make(map[string]*VWAPCalculator, len(futures))
	for _, sym := range futures {
		vwaps[sym] = NewVWAP(time.Minute)
	}
	return &Engine{
		vwaps:        vwaps,
		lastPrice:    make(map[string]float64),
		forexRates:   make(map[string]float64),
		officialRate: make(map[string]float64),
	}
}

// Ingest routes a tick to the appropriate internal cache or VWAP calculator.
func (e *Engine) Ingest(tick source.Tick) {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch tick.Symbol {
	case source.SymbolUSDRUBF, source.SymbolEURRUBF, source.SymbolCNYRUBF:
		// Only LAST prices feed VWAP; BID/ASK only update the price cache.
		if tick.Kind == source.KindLastPrice {
			e.vwaps[tick.Symbol].Add(tick.Price, tick.Volume, tick.Timestamp)
		}
		e.lastPrice[tick.Symbol] = tick.Price

	case source.SymbolEURUSD, source.SymbolUSDCNH:
		e.forexRates[tick.Symbol] = tick.Price

	case source.SymbolUSDRubOfficial, source.SymbolEURRubOfficial:
		e.officialRate[tick.Symbol] = tick.Price

	case source.SymbolUSDTRUB:
		e.lastPrice[tick.Symbol] = tick.Price
	}
}

// Snapshot computes and returns current funding values for all instruments.
func (e *Engine) Snapshot() FundingSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()
	return FundingSnapshot{
		Timestamp:    now,
		USDRUBF:      e.buildFunding(source.SymbolUSDRUBF, source.SymbolUSDRubOfficial, source.SymbolEURUSD, now),
		EURRUBF:      e.buildFunding(source.SymbolEURRUBF, source.SymbolEURRubOfficial, source.SymbolEURUSD, now),
		CNYRUBF:      e.buildCNYFunding(now),
		USDTRUBPrice: e.lastPrice[source.SymbolUSDTRUB],
	}
}

// buildFunding produces InstrumentFunding for USD/RUB and EUR/RUB futures.
func (e *Engine) buildFunding(sym, officialSym, forexSym string, now time.Time) InstrumentFunding {
	vwap, hasVWAP := e.vwaps[sym].Value(now)
	last := e.lastPrice[sym]

	inf := InstrumentFunding{
		VWAP:      vwap,
		LastPrice: last,
	}

	// MOEX funding: VWAP - last_price
	if hasVWAP && last > 0 {
		inf.MOEXFunding = ptr(vwap - last)
	}

	// Forex funding: VWAP - cross_rate
	// TODO: replace EURUSD proxy with actual spot USD/RUB or EUR/RUB once
	//       a direct forex feed is available (TwelveData free plan lacks USDRUB).
	if rate, ok := e.forexRates[forexSym]; ok && hasVWAP {
		inf.ForexFunding = ptr(vwap - rate)
	}

	// CB funding: VWAP - official_rate (available after CBR publishes daily rate)
	if rate, ok := e.officialRate[officialSym]; ok && hasVWAP {
		inf.CBFunding = ptr(vwap - rate)
		inf.OfficialRate = ptr(rate)
	}

	return inf
}

// buildCNYFunding produces InstrumentFunding for CNY/RUB futures.
// ForexFunding and CBFunding are not computed for CNYRUBF on MVP:
// no CNY/RUB direct forex feed and no CBR CNY rate in current sources.
func (e *Engine) buildCNYFunding(now time.Time) InstrumentFunding {
	vwap, hasVWAP := e.vwaps[source.SymbolCNYRUBF].Value(now)
	last := e.lastPrice[source.SymbolCNYRUBF]

	inf := InstrumentFunding{
		VWAP:      vwap,
		LastPrice: last,
	}
	// MOEX funding: VWAP - last_price
	// Per plan: "CNYRUBF — VWAP - last_price"
	if hasVWAP && last > 0 {
		inf.MOEXFunding = ptr(vwap - last)
	}
	// TODO: add ForexFunding when CNYRUB spot rate becomes available.
	// TODO: add CBFunding if CBR starts publishing CNY rate separately.
	return inf
}

func ptr(f float64) *float64 { return &f }
