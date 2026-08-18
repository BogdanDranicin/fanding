package robots

import "time"

// msk — биржевой день считается по московскому времени; фиксированное смещение,
// потому что в России нет перехода на летнее время.
var msk = time.FixedZone("MSK", 3*60*60)

// DayVolume — дневной оборот по инструменту, разложенный по стороне агрессора.
// Нужен, чтобы измерить силу робота: 300 лотов в час — это много на неликвиде и
// незаметно на SBER, и абсолютный объём сам по себе ни о чём не говорит.
type DayVolume struct {
	Symbol string  `json:"symbol"`
	Date   string  `json:"date"`     // биржевой день, YYYY-MM-DD MSK
	Buy    float64 `json:"buy"`      // лоты, купленные по оферу (агрессор — покупатель)
	Sell   float64 `json:"sell"`     // лоты, проданные по биду
	Trades int     `json:"trades"`   // сколько сделок учтено
	Since  string  `json:"since"`    // время первой учтённой сделки, HH:MM:SS MSK
	Latest string  `json:"latest"`   // время последней учтённой сделки
}

// Total — весь оборот дня в лотах.
func (v DayVolume) Total() float64 { return v.Buy + v.Sell }

// Side возвращает оборот на стороне робота: лонг сравниваем с покупками, шорт с продажами.
func (v DayVolume) Side(s Side) float64 {
	if s == SideSell {
		return v.Sell
	}
	return v.Buy
}

// dayVolumes копит оборот по всем лентам. Не потокобезопасен: живёт под мьютексом
// коллектора, как и детектор.
//
// Считаем по своей же ленте, а не по VOLTODAY из ISS: биржевая витрина не
// разделяет оборот на покупки и продажи, а нам нужна именно сторона. Плата за это —
// учитывается только то, что сервис успел увидеть; в проде он работает круглосуточно
// и к началу торгов уже стоит на ленте, поэтому за день охват полный. Время начала
// учёта отдаётся наружу (Since), чтобы после перезапуска среди дня было видно, от
// какой базы посчитана сила.
type dayVolumes struct {
	bySymbol map[string]*dayEntry
}

// dayEntry — счётчики инструмента плюс отсечка по номеру сделки. Отсечка нужна
// из-за пересева курсора: коллектор тогда перечитывает хвост ленты, и без неё те
// же сделки легли бы в оборот повторно, завысив базу для оценки силы.
type dayEntry struct {
	vol         DayVolume
	lastTradeNo int64
}

func newDayVolumes() *dayVolumes {
	return &dayVolumes{bySymbol: make(map[string]*dayEntry)}
}

// add учитывает принт. Смена биржевого дня обнуляет счётчики инструмента.
func (v *dayVolumes) add(p Print) {
	if p.Qty <= 0 || p.Time.IsZero() {
		return
	}
	local := p.Time.In(msk)
	date := local.Format("2006-01-02")

	e := v.bySymbol[p.Symbol]
	if e == nil || e.vol.Date != date {
		// Смена биржевого дня обнуляет и счётчики, и отсечку: номера сделок нового
		// дня не обязаны продолжать вчерашние.
		e = &dayEntry{vol: DayVolume{Symbol: p.Symbol, Date: date, Since: local.Format("15:04:05")}}
		v.bySymbol[p.Symbol] = e
	}
	if p.TradeNo > 0 && p.TradeNo <= e.lastTradeNo {
		return // эту сделку уже считали
	}

	switch p.Side {
	case SideBuy:
		e.vol.Buy += p.Qty
	case SideSell:
		e.vol.Sell += p.Qty
	default:
		return
	}
	if p.TradeNo > e.lastTradeNo {
		e.lastTradeNo = p.TradeNo
	}
	e.vol.Trades++
	e.vol.Latest = local.Format("15:04:05")
}

// get отдаёт оборот инструмента за день, к которому относится ts. Если накопленный
// день другой (сервис молчал через полночь), возвращается пустой — сила робота
// не должна считаться от вчерашней базы.
func (v *dayVolumes) get(symbol string, ts time.Time) DayVolume {
	e := v.bySymbol[symbol]
	if e == nil {
		return DayVolume{Symbol: symbol}
	}
	if !ts.IsZero() && e.vol.Date != ts.In(msk).Format("2006-01-02") {
		return DayVolume{Symbol: symbol}
	}
	return e.vol
}

// snapshot — копия оборотов по всем инструментам, для страницы.
func (v *dayVolumes) snapshot() []DayVolume {
	out := make([]DayVolume, 0, len(v.bySymbol))
	for _, e := range v.bySymbol {
		out = append(out, e.vol)
	}
	return out
}
