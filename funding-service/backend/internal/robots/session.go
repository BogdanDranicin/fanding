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

	// Misses — сколько тактов подряд робот промолчал к этому моменту. Один пропуск
	// подсвечивается на странице, два подряд убирают робота из списка совсем.
	Misses int `json:"misses"`

	// Поля ниже считаются на момент выдачи среза и в базу не едут.

	// NextBeatAt — когда ждать следующий принт (стенные часы, см. Robot.NextBeatAfter).
	NextBeatAt time.Time `json:"next_beat_at"`
	// PrintLots — объём робота за раз: сколько лотов проходит один его принт.
	PrintLots float64 `json:"print_lots"`
	// VolumeLots — сколько робот напечатал за всю серию.
	VolumeLots float64 `json:"volume_lots"`
	// DaySideLots — дневной оборот инструмента на стороне робота, для справки.
	DaySideLots float64 `json:"day_side_lots"`
	// HourLots — оборот инструмента за час, знаменатель силы.
	HourLots float64 `json:"hour_lots"`
	// HourMinutes — в скольких минутах окна вообще были сделки.
	HourMinutes int `json:"hour_minutes"`
	// HourFrom/HourTo — границы часового окна, MSK.
	HourFrom string `json:"hour_from"`
	HourTo   string `json:"hour_to"`
	// HourSince — с какого времени накоплен часовой оборот. Непустое означает, что
	// сбор начался внутри окна, час набран не целиком и сила завышена.
	HourSince string `json:"hour_since"`
	// LotsPerMin — сколько лотов робот прокачивает в минуту: его принт, делённый
	// на такт. Это и есть его вклад в поток бумаги, в отличие от разового объёма.
	LotsPerMin float64 `json:"lots_per_min"`
	// StrengthPct — сила робота: его поток в доле потока самой бумаги, проценты.
	// 0, если базы ещё нет.
	StrengthPct float64 `json:"strength_pct"`

	// dirty — с момента последней записи в базу сессия изменилась.
	dirty bool
}

// MaxMisses — сколько тактов подряд робот может промолчать, прежде чем его снимают
// со страницы. Пропуск такта — обычное дело (не нашлось ликвидности), а вот два
// подряд означают, что серия кончилась либо её и не было: на низком пороге
// обнаружения именно это правило и вычищает случайные совпадения размеров.
const MaxMisses = 2

// fill досчитывает поля, которые зависят от момента запроса и от оборота бумаги.
//
// Сила — это поток робота в доле потока бумаги: сколько лотов он прокачивает в
// минуту против того, сколько их за минуту проходит через инструмент вообще.
//
// Считать силу по одному принту, как было раньше, нельзя: принт в 300 лотов раз
// в три секунды и такой же принт раз в пять минут — это стократная разница в
// давлении на цену, а отношение «принт к часовому обороту» у них одинаковое.
// Час в знаменателе остаётся: за более короткое окно оборот скачет от одной
// крупной сделки, за более длинное — размывается сменой активности.
func (s *Session) fill(now time.Time, day DayVolume, hour HourVolume) {
	s.NextBeatAt = s.NextBeatAfter(now)
	s.PrintLots = s.QtyTypical
	s.VolumeLots = s.Volume()
	s.DaySideLots = day.Side(s.Side)
	s.HourLots = hour.Total()
	s.HourMinutes = hour.Minutes
	s.HourFrom, s.HourTo = hour.From, hour.To
	s.HourSince = hour.Since
	s.LotsPerMin = s.lotsPerMin()
	s.StrengthPct = strengthPct(s.LotsPerMin, s.HourLots)
}

// lotsPerMin — поток робота: принт, разложенный на такт.
func (s Session) lotsPerMin() float64 {
	if s.PeriodSec <= 0 {
		return 0
	}
	return s.PrintLots * 60 / s.PeriodSec
}

// strengthPct — доля потока бумаги, которую делает сам робот. Знаменатель —
// часовой оборот, приведённый к минуте.
func strengthPct(lotsPerMin, hourLots float64) float64 {
	if hourLots <= 0 || lotsPerMin <= 0 {
		return 0
	}
	return 100 * lotsPerMin / (hourLots / 60)
}

// DurationSec — сколько робот держится, по биржевым меткам его принтов.
func (s Session) DurationSec() float64 { return s.LastSeen.Sub(s.FirstSeen).Seconds() }

// registry сшивает находки сканов в сессии. Не потокобезопасен: вызывается из
// коллектора под его мьютексом.
type registry struct {
	sessions []*Session
	// closed — сессии, снятые со страницы в этом проходе; их ещё нужно дописать
	// в базу, поэтому они ждут здесь до ближайшего takeDirty.
	closed []*Session
	// beatTol — допуск на такт; тот же, с каким детектор раскладывал интервалы.
	beatTol func(periodSec float64) time.Duration
	// dropped — роботы, только что снятые за пропуски. Держим их в чёрном списке
	// на время окна анализа: снятая серия ещё лежит в ленте, и без этого следующий
	// же скан находил бы её заново и заводил новую сессию каждые пятнадцать секунд.
	dropped       []droppedRobot
	dropRetention time.Duration
	// staleAfter — сколько робот может молчать, прежде чем сессию закрывают.
	staleAfter time.Duration
	// keepClosed — сколько закрытая сессия ещё висит в памяти (для страницы истории
	// свежие закрытые роботы важнее старых).
	keepClosed time.Duration
	maxKeep    int
}

// droppedRobot — снятая серия и момент снятия.
type droppedRobot struct {
	robot Robot
	at    time.Time
}

func newRegistry(staleAfter, keepClosed, dropRetention time.Duration, maxKeep int, beatTol func(float64) time.Duration) *registry {
	return &registry{
		staleAfter:    staleAfter,
		keepClosed:    keepClosed,
		dropRetention: dropRetention,
		maxKeep:       maxKeep,
		beatTol:       beatTol,
	}
}

// observe вливает результат очередного скана в реестр. Изменившиеся сессии
// помечаются как dirty; забрать их для записи в базу можно через takeDirty.
//
// head — время самого свежего принта в ленте каждого тикера. По нему, а не по
// стенным часам, считаются пропущенные такты: публичная лента ISS запаздывает на
// минуты, и по часам работающий робот выглядел бы молчащим все эти минуты.
func (r *registry) observe(found []Robot, now time.Time, head map[string]time.Time) {
	r.forgetDropped(now)

	for _, rb := range found {
		if s := r.match(rb); s != nil {
			// Начало серии всегда самое раннее из виденных: окно анализа съезжает
			// вперёд и в одиночку показало бы робота «начавшимся» только что.
			first := s.FirstSeen
			priceFirst := s.PriceFirst
			if rb.FirstSeen.Before(first) {
				first, priceFirst = rb.FirstSeen, rb.PriceFirst
			}
			printed := rb.LastSeen.After(s.LastSeen)
			s.Robot = rb
			s.Robot.FirstSeen = first
			s.Robot.PriceFirst = priceFirst
			s.QtyMin = min(s.QtyMin, rb.QtyMin)
			s.QtyMax = max(s.QtyMax, rb.QtyMax)
			s.UpdatedAt = now
			s.Active = true
			s.dirty = true
			if printed {
				// Робот отработал такт — счётчик пропусков начинается заново.
				s.Misses = 0
			}
			continue
		}
		if r.wasDropped(rb) {
			continue
		}
		r.sessions = append(r.sessions, &Session{
			Robot: rb, DetectedAt: now, UpdatedAt: now, Active: true, dirty: true,
		})
	}

	r.markMisses(now, head)

	// Робот, который перестал печатать, закрывается — но остаётся в истории.
	for _, s := range r.sessions {
		if s.Active && r.silent(s, now, head[s.Symbol]) {
			s.Active = false
			s.UpdatedAt = now
			s.dirty = true
		}
	}

	r.evict(now)
}

// feedSilence — сколько лента бумаги может не двигаться, прежде чем роботы на ней
// закрываются. Больше запаздывания публичного фида ISS (ровно пятнадцать минут),
// иначе инструмент, идущий только по нему, закрывался бы сразу; но и не бесконечно:
// у бумаги, переставшей торговаться, лента замирает, и без этой границы её роботы
// висели бы на странице до конца дня.
const feedSilence = 20 * time.Minute

// silent — молчит ли робот.
//
// Меряется по времени ленты его бумаги, а не по стенным часам: у инструмента вне
// быстрого источника лента отстаёт на пятнадцать минут, и по стенным часам такой
// робот считался бы замолчавшим с самого рождения — находка попадала в историю,
// но на странице «Сейчас» не появлялась ни разу. Ровно так же меряются и
// пропущенные такты (см. missedBeats).
func (r *registry) silent(s *Session, now, head time.Time) bool {
	if head.IsZero() {
		return now.Sub(s.LastSeen) > r.staleAfter
	}
	if now.Sub(head) > feedSilence {
		return true // лента бумаги встала целиком
	}
	return head.Sub(s.LastSeen) > r.staleAfter
}

// markMisses проставляет пропущенные такты и снимает со страницы тех, кто
// промолчал MaxMisses тактов подряд.
func (r *registry) markMisses(now time.Time, head map[string]time.Time) {
	kept := r.sessions[:0]
	for _, s := range r.sessions {
		missed := r.missedBeats(s, head[s.Symbol])
		if missed != s.Misses {
			s.Misses = missed
			s.dirty = true
		}
		if s.Active && missed >= MaxMisses {
			s.Active = false
			s.UpdatedAt = now
			s.dirty = true
			// Со страницы убираем, но в базу дописать обязаны: строка истории
			// должна закрыться, а не остаться висеть активной.
			r.closed = append(r.closed, s)
			r.dropped = append(r.dropped, droppedRobot{robot: s.Robot, at: now})
			continue
		}
		kept = append(kept, s)
	}
	r.sessions = kept
}

// missedBeats — сколько тактов робот пропустил к моменту, до которого дошла лента.
// Пока лента не перешагнула ожидаемый такт с допуском, пропуска нет.
func (r *registry) missedBeats(s *Session, head time.Time) int {
	if head.IsZero() || s.PeriodSec <= 0 || s.LastSeen.IsZero() {
		return 0
	}
	period := time.Duration(s.PeriodSec * float64(time.Second))
	if period <= 0 {
		return 0
	}
	var tol time.Duration
	if r.beatTol != nil {
		tol = r.beatTol(s.PeriodSec)
	}
	elapsed := head.Sub(s.LastSeen) - tol
	if elapsed < period {
		return 0
	}
	return int(elapsed / period)
}

// wasDropped — снимали ли мы такую серию недавно.
func (r *registry) wasDropped(rb Robot) bool {
	for _, d := range r.dropped {
		if SameRobot(d.robot, rb) {
			return true
		}
	}
	return false
}

// forgetDropped чистит чёрный список: серия успела уехать из окна анализа.
func (r *registry) forgetDropped(now time.Time) {
	kept := r.dropped[:0]
	for _, d := range r.dropped {
		if now.Sub(d.at) <= r.dropRetention {
			kept = append(kept, d)
		}
	}
	r.dropped = kept
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

// takeDirty возвращает изменившиеся сессии и снимает с них пометку. Сюда же
// попадают снятые за пропуски — их надо дописать в базу последним состоянием.
func (r *registry) takeDirty() []*Session {
	var out []*Session
	for _, s := range r.sessions {
		if s.dirty {
			s.dirty = false
			out = append(out, s)
		}
	}
	for _, s := range r.closed {
		if s.dirty {
			s.dirty = false
			out = append(out, s)
		}
	}
	r.closed = nil
	return out
}
