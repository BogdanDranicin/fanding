package source

import "time"

// TickKind describes what price a Tick represents.
type TickKind int

const (
	KindLastPrice   TickKind = iota
	KindBid
	KindAsk
	KindOfficialRate
	KindVWAP
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
