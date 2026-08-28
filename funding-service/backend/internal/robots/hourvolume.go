package robots

import "time"

// HourVolume — оборот по инструменту за последний час, разложенный по стороне
// агрессора. Это знаменатель силы робота: один принт в 300 лотов — это половина
// часового оборота неликвида и незаметная рябь на SBER.
//
// Час, а не день: сила должна отвечать на вопрос «сколько робот сейчас весит в
// бумаге». Дневной оборот к вечеру набирает такую величину, что любой робот на
// его фоне выглядит слабым, и различить сильного от слабого по нему нельзя.
type HourVolume struct {
	Symbol string  `json:"symbol"`
	Buy    float64 `json:"buy"`    // лоты, купленные по оферу (агрессор — покупатель)
	Sell   float64 `json:"sell"`   // лоты, проданные по биду
	Trades int     `json:"trades"` // сколько сделок легло в час
	// From/To — границы окна, биржевое время MSK, HH:MM. Пустые, если оборота нет.
	From string `json:"from"`
	To   string `json:"to"`
	// Minutes — в скольких минутах окна вообще были сделки. На неликвиде их единицы,
	// и это само по себе говорит, насколько оборот часа опирается на редкие сделки.
	Minutes int `json:"minutes"`
	// Since — с какого времени сервис вообще видит ленту этой бумаги, MSK. Пустое,
	// если сбор начался раньше окна, то есть час набран целиком. Заполненное
	// означает, что окно накрыто данными не полностью и сила по нему завышена.
	Since string `json:"since"`
}

// Total — весь оборот часа в лотах.
func (v HourVolume) Total() float64 { return v.Buy + v.Sell }

// hourWindow — длина окна, за которое считается оборот.
const hourWindow = 60

// ringMinutes — глубина кольца минутных корзин. Больше окна намеренно: окно
// отсчитывается не от стенных часов, а от последнего принта робота, и по
// инструменту с медленной лентой якорь отстаёт от текущей минуты. Запас в час
// покрывает и пятнадцатиминутное запаздывание публичного фида ISS, и паузу в
// торгах, после которой лента возвращается к прежней бумаге.
const ringMinutes = 120

// hourVolumes копит минутные корзины оборота по всем инструментам. Не
// потокобезопасен: живёт под мьютексом коллектора, как детектор и дневной оборот.
type hourVolumes struct {
	bySymbol map[string]*hourEntry
}

// hourEntry — кольцо минутных корзин инструмента плюс отсечка по номеру сделки.
// Отсечка та же, что у дневного оборота, и по той же причине: при пересеве
// курсора коллектор перечитывает хвост ленты, и без неё сделки легли бы дважды.
type hourEntry struct {
	ring        [ringMinutes]hourBucket
	lastTradeNo int64
	// firstSeen — самый ранний принт бумаги, который сервис вообще видел. По нему
	// видно, набран ли час целиком: после старта среди торгов — нет.
	firstSeen time.Time
}

// hourBucket — оборот одной биржевой минуты. minute — абсолютный номер минуты
// (Unix/60), по нему корзина отличает свою минуту от чужой, затёртой кольцом.
type hourBucket struct {
	minute int64
	buy    float64
	sell   float64
	trades int
}

func newHourVolumes() *hourVolumes {
	return &hourVolumes{bySymbol: make(map[string]*hourEntry)}
}

// add учитывает принт в корзине его биржевой минуты.
func (v *hourVolumes) add(p Print) {
	if p.Qty <= 0 || p.Time.IsZero() {
		return
	}
	if p.Side != SideBuy && p.Side != SideSell {
		return
	}

	e := v.bySymbol[p.Symbol]
	if e == nil {
		e = &hourEntry{}
		v.bySymbol[p.Symbol] = e
	}
	if p.TradeNo > 0 && p.TradeNo <= e.lastTradeNo {
		return // эту сделку уже считали
	}

	if e.firstSeen.IsZero() || p.Time.Before(e.firstSeen) {
		e.firstSeen = p.Time
	}

	minute := p.Time.Unix() / 60
	b := &e.ring[((minute%ringMinutes)+ringMinutes)%ringMinutes]
	if b.minute != minute {
		*b = hourBucket{minute: minute}
	}
	if p.Side == SideBuy {
		b.buy += p.Qty
	} else {
		b.sell += p.Qty
	}
	b.trades++
	if p.TradeNo > e.lastTradeNo {
		e.lastTradeNo = p.TradeNo
	}
}

// get отдаёт оборот инструмента за час, закончившийся в ts.
//
// Якорь — момент последнего принта робота, а не стенные часы: источники ленты
// приходят с разной задержкой (поток брокера отстаёт на секунды, публичный фид
// ISS на пятнадцать минут), и окно от стенных часов у бумаги на ISS было бы
// пустым в своей свежей четверти. От последнего принта окно у всех полное, и
// сила инструментов сравнима между собой.
func (v *hourVolumes) get(symbol string, ts time.Time) HourVolume {
	return v.window(symbol, ts, hourWindow)
}

// window — то же самое за произвольное число последних минут.
//
// Нужно для силы робота: сравнивать его поток с часовым оборотом можно только
// если робот этот час и работал. Робот, начавший пять минут назад, получал по
// часовому знаменателю тысячи процентов — его минутный поток растягивался на час,
// которого не было. Окно сравнения обрезается до времени жизни серии, и обе части
// отношения снова меряют одно и то же время.
func (v *hourVolumes) window(symbol string, ts time.Time, minutes int) HourVolume {
	out := HourVolume{Symbol: symbol}
	e := v.bySymbol[symbol]
	if e == nil || ts.IsZero() {
		return out
	}
	if minutes < 1 {
		minutes = 1
	}
	if minutes > hourWindow {
		minutes = hourWindow
	}

	last := ts.Unix() / 60
	first := last - int64(minutes) + 1
	for i := range e.ring {
		b := e.ring[i]
		if b.minute < first || b.minute > last {
			continue
		}
		out.Buy += b.buy
		out.Sell += b.sell
		out.Trades += b.trades
		out.Minutes++
	}
	if out.Minutes > 0 {
		out.From = time.Unix(first*60, 0).In(msk).Format("15:04")
		out.To = time.Unix(last*60, 0).In(msk).Format("15:04")
	}
	if !e.firstSeen.IsZero() && e.firstSeen.Unix()/60 > first {
		out.Since = e.firstSeen.In(msk).Format("15:04:05")
	}
	return out
}
