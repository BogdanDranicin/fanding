package robots

import (
	"sort"
	"time"
)

// Session — жизнь одного робота во времени. Скан видит робота только в пределах
// окна анализа; сессия сшивает последовательные находки в одну строку, чтобы на
// странице был «робот, работающий с 11:04», а не новая запись каждые полминуты.
type Session struct {
	// ID — идентификатор строки в базе; 0 у ещё не сохранённой сессии.
	ID int64 `json:"id"`
	Robot
	// DetectedAt — когда сервис впервые увидел эту серию (может быть позже
	// FirstSeen: FirstSeen — время самого раннего принта серии на бирже).
	DetectedAt time.Time `json:"detected_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Active     bool      `json:"active"`
	// dirty — с момента последней записи в базу сессия изменилась.
	dirty bool
}

// DurationSec — сколько робот держится, по биржевым меткам его принтов.
func (s Session) DurationSec() float64 { return s.LastSeen.Sub(s.FirstSeen).Seconds() }

// registry сшивает находки сканов в сессии. Не потокобезопасен: вызывается из
// коллектора под его мьютексом.
type registry struct {
	sessions []*Session
	// staleAfter — сколько робот может молчать, прежде чем сессию закрывают.
	staleAfter time.Duration
	// keepClosed — сколько закрытая сессия ещё висит в памяти (для страницы истории
	// свежие закрытые роботы важнее старых).
	keepClosed time.Duration
	maxKeep    int
}

func newRegistry(staleAfter, keepClosed time.Duration, maxKeep int) *registry {
	return &registry{staleAfter: staleAfter, keepClosed: keepClosed, maxKeep: maxKeep}
}

// observe вливает результат очередного скана в реестр. Изменившиеся сессии
// помечаются как dirty; забрать их для записи в базу можно через takeDirty.
func (r *registry) observe(found []Robot, now time.Time) {
	for _, rb := range found {
		if s := r.match(rb); s != nil {
			// Начало серии всегда самое раннее из виденных: окно анализа съезжает
			// вперёд и в одиночку показало бы робота «начавшимся» только что.
			first := s.FirstSeen
			priceFirst := s.PriceFirst
			if rb.FirstSeen.Before(first) {
				first, priceFirst = rb.FirstSeen, rb.PriceFirst
			}
			s.Robot = rb
			s.Robot.FirstSeen = first
			s.Robot.PriceFirst = priceFirst
			s.QtyMin = min(s.QtyMin, rb.QtyMin)
			s.QtyMax = max(s.QtyMax, rb.QtyMax)
			s.UpdatedAt = now
			s.Active = true
			s.dirty = true
			continue
		}
		r.sessions = append(r.sessions, &Session{
			Robot: rb, DetectedAt: now, UpdatedAt: now, Active: true, dirty: true,
		})
	}

	// Робот, который перестал печатать, закрывается — но остаётся в истории.
	for _, s := range r.sessions {
		if s.Active && now.Sub(s.LastSeen) > r.staleAfter {
			s.Active = false
			s.UpdatedAt = now
			s.dirty = true
		}
	}

	r.evict(now)
}

// match ищет активную сессию, описывающую того же робота.
func (r *registry) match(rb Robot) *Session {
	for _, s := range r.sessions {
		if s.Active && SameRobot(s.Robot, rb) {
			return s
		}
	}
	return nil
}

// evict убирает из памяти давно закрытые сессии; в базе они остаются.
func (r *registry) evict(now time.Time) {
	kept := r.sessions[:0]
	for _, s := range r.sessions {
		if !s.Active && now.Sub(s.UpdatedAt) > r.keepClosed {
			continue
		}
		kept = append(kept, s)
	}
	r.sessions = kept

	if r.maxKeep > 0 && len(r.sessions) > r.maxKeep {
		sort.Slice(r.sessions, func(i, j int) bool {
			return r.sessions[i].UpdatedAt.After(r.sessions[j].UpdatedAt)
		})
		r.sessions = r.sessions[:r.maxKeep]
	}
}

// snapshot отдаёт копию сессий: активные первыми, внутри — по убыванию уверенности.
func (r *registry) snapshot() []Session {
	out := make([]Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out
}

// takeDirty возвращает изменившиеся сессии и снимает с них пометку.
func (r *registry) takeDirty() []*Session {
	var out []*Session
	for _, s := range r.sessions {
		if s.dirty {
			s.dirty = false
			out = append(out, s)
		}
	}
	return out
}
