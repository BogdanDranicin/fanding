package source

import "time"

// TickKind describes what price a Tick represents.
type TickKind int

const (
	KindLastPrice TickKind = iota
	KindBid
	KindAsk
	KindOfficialRate    // startup load of current official rate (yesterday's publication)
	KindNewOfficialRate // fresh intraday publication from CBR (16:30–18:00 MSK)
	KindVWAP
	KindSwapRate
	KindSettlePrice // official MOEX settlement price published after 15:30 MSK
	KindWaprice     // session VWAP published by MOEX ISS (WAPRICE field)
	KindTrade       // single executed deal from MOEX ISS trades.json (Volume = QUANTITY of that trade)
	KindPrevSettle  // расчётная цена предыдущего вечернего клиринга (PREVSETTLEPRICE) — база границ K1/K2

	// KindStreamUp / KindStreamDown — служебные тики живого потока сделок: подписка
	// установлена / оборвалась. Цены не несут. Движку они нужны, чтобы знать, покрыт
	// ли живым потоком ВЕСЬ расчётный интервал 10:00–15:30: поток, поднявшийся в
	// полдень или переподключившийся среди окна, теряет сделки безвозвратно —
	// в отличие от ленты ISS, которая доигрывает пропущенное по курсору TRADENO.
	KindStreamUp
	KindStreamDown
)

// Symbol constants for all tracked instruments.
const (
	SymbolUSDRUBF        = "USDRUBF"
	SymbolEURRUBF        = "EURRUBF"
	SymbolCNYRUBF        = "CNYRUBF"
	SymbolUSDTRUB        = "USDTRUB"
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

	// Backdated marks a KindTrade whose deal happened on ANOTHER calendar day but
	// which the exchange booked into the current trading session (TRADEDATE !=
	// TRADE_SESSION_DATE): weekend deals published at the Monday morning technical
	// session, previous-day evening deals, and the like. Their volume counts toward
	// VOLTODAY, but their price belongs to another day and must stay out of every
	// price accumulator.
	Backdated bool

	// Live помечает сделку, пришедшую живым потоком брокера, а не публичной лентой
	// MOEX ISS. Разница не в качестве данных, а в возрасте: лента ISS отдаёт сделку
	// через пятнадцать минут после того, как она прошла на бирже, поток брокера —
	// через десяток миллисекунд (замер на проде 27.08.2026: lag_ms = 12).
	//
	// Для ноги фьючерса это решает всё. Окно 10:00–15:30 по ленте ISS закрывается
	// только к 15:45, и до тех пор движок не может отличить «окно кончилось» от
	// «лента отстала», из-за чего нога регулярно замораживалась обрезанной. Живой
	// поток пересекает 15:30 своими же сделками секунда в секунду.
	Live bool
}
