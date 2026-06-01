package source

import "time"

// TickKind describes what price a Tick represents.
type TickKind int

const (
	KindLastPrice   TickKind = iota
	KindBid
	KindAsk
	KindOfficialRate    // startup load of current official rate (yesterday's publication)
	KindNewOfficialRate // fresh intraday publication from CBR (16:30–18:00 MSK)
	KindVWAP
	KindSwapRate
	KindSettlePrice // official MOEX settlement price published after 15:30 MSK
	KindWaprice     // session VWAP published by MOEX ISS (WAPRICE field)
)

// Symbol constants for all tracked instruments.
const (
	SymbolUSDRUBF       = "USDRUBF"
	SymbolEURRUBF       = "EURRUBF"
	SymbolCNYRUBF       = "CNYRUBF"
	SymbolUSDTRUB       = "USDTRUB"
	SymbolEURUSD        = "EURUSD"
	SymbolUSDCNH        = "USDCNH"
	SymbolUSDRubOfficial = "USDRUB_CB"
	SymbolEURRubOfficial = "EURRUB_CB"

	// Spot FX with "tomorrow" settlement on MOEX (engine=currency, market=selt, board=CETS).
	// Their volume-weighted price over 10:00–15:30 MSK is the basis for the CBR official rate.
	SymbolUSDRubTOM = "USDRUB_TOM"
)

// Tick is the unified internal price event produced by any MarketDataSource.
type Tick struct {
	Symbol    string
	Price     float64
	Volume    float64
	Kind      TickKind
	Timestamp time.Time
	Source    string
}
