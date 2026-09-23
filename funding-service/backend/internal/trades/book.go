package trades

import (
	"strconv"
	"strings"
	"time"
)

type Status string

const (
	StatusOpen   Status = "open"
	StatusClosed Status = "closed"
)

// Position — позиция одного автора по одному инструменту.
type Position struct {
	ID         int64      `json:"id"`
	Author     string     `json:"author"`
	Book       string     `json:"book"`
	Ticker     string     `json:"ticker"`
	Direction  Direction  `json:"direction"`
	Size       string     `json:"size"`
	EntryPrice *float64   `json:"entry_price"`
	Stop       string     `json:"stop"`
	Targets    string     `json:"targets"`
	Status     Status     `json:"status"`
	OpenedAt   time.Time  `json:"opened_at"`
	ClosedAt   *time.Time `json:"closed_at"`
	ClosePrice *float64   `json:"close_price"`
	Note       string     `json:"note"`
	Manual     bool       `json:"manual"`
	UpdatedAt  time.Time  `json:"updated_at"`
	LastText   string     `json:"last_text"`
}

// Change — что сделать с позицией по одному сигналу. Отрицательный
// Position.ID — позиция, заведённая этим же сообщением и ещё не записанная.
type Change struct {
	Position Position
	Action   Action
	Signal   Signal
}

// Ledger — открытые позиции одного автора и память о закрытых: по ней «вернул
// 4000 самика» после закрытого шорта снова открывает шорт, а не лонг.
type Ledger struct {
	Author     string
	Open       []Position
	LastClosed map[string]Direction
	tmpID      int64
}

// Apply применяет сигналы одного сообщения по порядку и возвращает изменения.
// Позиции в Ledger обновляются по ходу, так что второй сигнал того же
// сообщения видит результат первого.
func (l *Ledger) Apply(sigs []Signal, at time.Time, text string) []Change {
	var out []Change
	for _, s := range sigs {
		out = append(out, l.apply(s, at, text)...)
	}
	return out
}

func (l *Ledger) apply(s Signal, at time.Time, text string) []Change {
	switch s.Action {
	case ActCloseAll:
		var out []Change
		for i := range l.Open {
			p := l.Open[i]
			if s.Direction != DirNone && p.Direction != s.Direction {
				continue
			}
			out = append(out, l.close(i, s, at, text))
		}
		l.compact()
		return out

	case ActOpen:
		if i := l.find(s); i >= 0 {
			p := l.Open[i]
			if s.Direction == DirNone || p.Direction == s.Direction {
				return []Change{l.add(i, s, at, text, ActAdd)}
			}
			out := []Change{l.close(i, s, at, text)}
			l.compact()
			return append(out, l.create(s, s.Direction, at, text))
		}
		dir := s.Direction
		if dir == DirNone {
			dir = DirLong
		}
		return []Change{l.create(s, dir, at, text)}

	case ActAdd:
		if i := l.find(s); i >= 0 {
			return []Change{l.add(i, s, at, text, ActAdd)}
		}
		if s.Ticker == "" {
			return nil
		}
		dir := s.Direction
		if dir == DirNone {
			dir = l.LastClosed[s.Ticker]
		}
		if dir == DirNone {
			dir = DirLong
		}
		c := l.create(s, dir, at, text)
		c.Action = ActOpen
		return []Change{c}

	case ActReduce:
		i := l.find(s)
		if i < 0 {
			return nil
		}
		p := &l.Open[i]
		if s.Size != "" {
			p.Size = subtractSize(p.Size, s.Size)
		} else {
			p.Size = strings.TrimSpace(p.Size + " (сокращена)")
		}
		l.touch(p, s, at, text)
		return []Change{{Position: *p, Action: ActReduce, Signal: s}}

	case ActClose:
		i := l.find(s)
		if i < 0 {
			return nil
		}
		c := l.close(i, s, at, text)
		l.compact()
		return []Change{c}

	case ActStop:
		i := l.find(s)
		if i < 0 {
			return nil
		}
		p := &l.Open[i]
		if s.Stop != "" {
			p.Stop = s.Stop
		}
		if s.Targets != "" {
			p.Targets = s.Targets
		}
		p.UpdatedAt = at
		return []Change{{Position: *p, Action: ActStop, Signal: s}}
	}
	return nil
}

// find ищет открытую позицию под сигнал. Без тикера — единственная открытая
// позиция автора (с учётом стороны, если она названа): «Шорт закрыл» у автора
// с одним шортом однозначно, с двумя — нет, и тогда ничего не трогаем.
func (l *Ledger) find(s Signal) int {
	var cands []int
	for i, p := range l.Open {
		if p.Status != StatusOpen {
			continue
		}
		if s.Ticker == "" {
			if s.Direction == DirNone || p.Direction == s.Direction {
				cands = append(cands, i)
			}
			continue
		}
		if tickerMatches(p.Ticker, s.Ticker) {
			cands = append(cands, i)
		}
	}
	if s.Ticker == "" {
		if len(cands) == 1 {
			return cands[0]
		}
		return -1
	}
	if s.Book != "" {
		var same []int
		for _, i := range cands {
			if l.Open[i].Book == s.Book {
				same = append(same, i)
			}
		}
		if len(same) > 0 {
			cands = same
		}
	}
	best := -1
	for _, i := range cands {
		if best < 0 || l.Open[i].UpdatedAt.After(l.Open[best].UpdatedAt) {
			best = i
		}
	}
	return best
}

func tickerMatches(have, want string) bool {
	if strings.EqualFold(have, want) {
		return true
	}
	return want == "ОФЗ" && strings.HasPrefix(have, "ОФЗ")
}

func (l *Ledger) create(s Signal, dir Direction, at time.Time, text string) Change {
	l.tmpID--
	p := Position{
		ID:         l.tmpID,
		Author:     l.Author,
		Book:       s.Book,
		Ticker:     s.Ticker,
		Direction:  dir,
		Size:       strings.TrimPrefix(s.Size, "до "),
		EntryPrice: s.Price,
		Stop:       s.Stop,
		Targets:    s.Targets,
		Status:     StatusOpen,
		OpenedAt:   at,
		UpdatedAt:  at,
		LastText:   text,
	}
	l.Open = append(l.Open, p)
	return Change{Position: p, Action: ActOpen, Signal: s}
}

func (l *Ledger) add(i int, s Signal, at time.Time, text string, a Action) Change {
	p := &l.Open[i]
	p.Size = addSize(p.Size, s.Size)
	if p.EntryPrice == nil {
		p.EntryPrice = s.Price
	}
	l.touch(p, s, at, text)
	return Change{Position: *p, Action: a, Signal: s}
}

func (l *Ledger) touch(p *Position, s Signal, at time.Time, text string) {
	if s.Stop != "" {
		p.Stop = s.Stop
	}
	if s.Targets != "" {
		p.Targets = s.Targets
	}
	if p.Book == "" {
		p.Book = s.Book
	}
	p.UpdatedAt = at
	p.LastText = text
}

func (l *Ledger) close(i int, s Signal, at time.Time, text string) Change {
	p := &l.Open[i]
	p.Status = StatusClosed
	t := at
	p.ClosedAt = &t
	p.ClosePrice = s.Price
	p.UpdatedAt = at
	p.LastText = text
	if l.LastClosed == nil {
		l.LastClosed = map[string]Direction{}
	}
	l.LastClosed[p.Ticker] = p.Direction
	return Change{Position: *p, Action: ActClose, Signal: s}
}

func (l *Ledger) compact() {
	open := l.Open[:0]
	for _, p := range l.Open {
		if p.Status == StatusOpen {
			open = append(open, p)
		}
	}
	l.Open = open
}

type sizeVal struct {
	v    float64
	unit string
}

func parseSize(s string) (sizeVal, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "до "))
	if s == "" {
		return sizeVal{}, false
	}
	if strings.HasSuffix(s, "%") {
		v, ok := parseNumber(strings.TrimSuffix(s, "%"))
		return sizeVal{v, "%"}, ok
	}
	parts := strings.Fields(s)
	v, ok := parseNumber(parts[0])
	if !ok {
		return sizeVal{}, false
	}
	unit := "шт"
	if len(parts) > 1 {
		unit = parts[1]
	}
	return sizeVal{v, unit}, len(parts) <= 2
}

func formatSize(v sizeVal) string {
	n := strconv.FormatFloat(v.v, 'f', -1, 64)
	if v.unit == "%" {
		return n + "%"
	}
	return n + " " + v.unit
}

// upTo — «до 6000» у позиции «4000 шт» значит 6000 штук: единицу берём у позиции.
func upTo(have, delta string) string {
	target := strings.TrimPrefix(delta, "до ")
	a, okA := parseSize(have)
	b, okB := parseSize(target)
	if okA && okB && len(strings.Fields(target)) == 1 && !strings.HasSuffix(target, "%") {
		b.unit = a.unit
		return formatSize(b)
	}
	return target
}

func addSize(have, delta string) string {
	if delta == "" {
		return have
	}
	if strings.HasPrefix(delta, "до ") {
		return upTo(have, delta)
	}
	if have == "" {
		return delta
	}
	a, okA := parseSize(have)
	b, okB := parseSize(delta)
	if okA && okB && a.unit == b.unit {
		return formatSize(sizeVal{a.v + b.v, a.unit})
	}
	return have + " + " + delta
}

func subtractSize(have, delta string) string {
	if strings.HasPrefix(delta, "до ") {
		return upTo(have, delta)
	}
	a, okA := parseSize(have)
	b, okB := parseSize(delta)
	if okA && okB && a.unit == b.unit && a.v > b.v {
		return formatSize(sizeVal{a.v - b.v, a.unit})
	}
	if have == "" {
		return "−" + delta
	}
	return have + " − " + delta
}
