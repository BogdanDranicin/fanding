package robots

import "time"

// Лента обезличенных сделок одного инструмента — то же, что в терминале, только
// со склейкой приказов.
//
// Биржа печатает не приказы, а сделки: рыночный приказ на 372 лота выходит в
// ленту пятью строчками. В терминале эти строчки так и лежат пятью, и приказ
// в них глазами не собрать. Здесь они складываются тем же mergeAggressors, что
// кормит поиск роботов, — и в ленте видно приказ, а не его осколки.

// TapePrint — одна строка ленты для страницы.
type TapePrint struct {
	Time  time.Time `json:"time"`
	Price float64   `json:"price"`
	// Qty — объём в лотах. У склеенного принта это объём всего приказа.
	Qty  float64 `json:"qty"`
	Side Side    `json:"side"`
	// Trades — сколько сделок биржи в этой строке. Единица — приказ забрал одну
	// заявку целиком.
	Trades int `json:"trades"`
}

// TapeCopy — копия ленты тикера. Именно копия: лента живёт под мьютексом
// коллектора и продолжает расти, пока ответ уходит в сеть.
func (d *Detector) TapeCopy(symbol string) []Print {
	tape := d.tapes[symbol]
	if len(tape) == 0 {
		return nil
	}
	return append([]Print(nil), tape...)
}

// Tape отдаёт последние принты инструмента, свежие впереди — как в терминале.
//
// merged=false показывает сырые сделки биржи: по ним сверяют, что склейка не
// слепила вместе два разных приказа.
func (c *Collector) Tape(symbol string, limit int, merged bool) []TapePrint {
	c.mu.Lock()
	tape := c.det.TapeCopy(symbol)
	gap, span := c.opts.Detector.AggressorGap, c.opts.Detector.AggressorSpan
	c.mu.Unlock()

	if len(tape) == 0 {
		return []TapePrint{}
	}
	if merged && gap > 0 {
		tape = mergeAggressors(tape, gap, span)
	}
	if limit > 0 && len(tape) > limit {
		tape = tape[len(tape)-limit:]
	}

	out := make([]TapePrint, 0, len(tape))
	for i := len(tape) - 1; i >= 0; i-- {
		p := tape[i]
		trades := p.Trades
		if trades == 0 {
			trades = 1
		}
		out = append(out, TapePrint{
			Time:   p.Time,
			Price:  p.Price,
			Qty:    p.Qty,
			Side:   p.Side,
			Trades: trades,
		})
	}
	return out
}
