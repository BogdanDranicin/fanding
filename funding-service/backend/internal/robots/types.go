// Package robots ищет в ленте сделок MOEX торговых роботов: повторяющиеся
// принты одного размера, идущие через равные промежутки времени.
//
// Робот описывается четырьмя наблюдаемыми величинами — тикер, направление
// (агрессор покупал или продавал), лотовка (размер принта) и тайминг (период
// повторения). Пример: по SBER каждые 11.2 с проходит покупка 3011–3013 лотов.
package robots

import (
	"math"
	"time"
)

// Side — направление агрессора в сделке (колонка BUYSELL в ленте ISS).
// Лента показывает сторону инициатора: B — робот берёт по оферу (лонг),
// S — робот льёт по биду (шорт).
type Side string

const (
	SideBuy  Side = "B"
	SideSell Side = "S"
)

// Print — одна сделка из ленты биржи.
type Print struct {
	TradeNo int64
	Symbol  string
	// Time — биржевое время сделки (TRADEDATE+TRADETIME), не момент получения:
	// публичный фид ISS запаздывает на минуты, и по времени прихода периодичность
	// восстановить нельзя. Разрешение биржевой метки — одна секунда.
	Time  time.Time
	Price float64
	Qty   float64 // объём сделки в лотах
	Side  Side
}

// Robot — обнаруженная закономерность в ленте одного тикера.
type Robot struct {
	Symbol string `json:"symbol"`
	Side   Side   `json:"side"`

	// Лотовка: диапазон размеров принтов, попавших в кластер, и типичный размер.
	QtyMin     float64 `json:"qty_min"`
	QtyMax     float64 `json:"qty_max"`
	QtyTypical float64 `json:"qty_typical"`

	// Тайминг: период повторения в секундах и разброс интервалов вокруг него
	// (коэффициент вариации; у настоящего робота он околонулевой).
	PeriodSec float64 `json:"period_sec"`
	Jitter    float64 `json:"jitter"`

	Prints     int     `json:"prints"`     // сколько принтов в кластере
	Beats      int     `json:"beats"`      // сколько тактов периода уложилось в серию
	Confidence float64 `json:"confidence"` // 0..1, насколько уверенно это робот

	// Provisional — серия ещё короче той, на которой периодичность отличима от
	// случайного совпадения (см. ConfidentPrints). Такой робот показывается сразу,
	// как просили, но помечен: подтвердит он себя или отвалится по пропущенному
	// такту, станет ясно за следующие один-два такта.
	Provisional bool `json:"provisional"`

	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`

	PriceFirst float64 `json:"price_first"`
	PriceLast  float64 `json:"price_last"`
}

// ConfidentPrints — длина серии, начиная с которой периодичность уже не объясняется
// случайным совпадением размеров. Ниже неё робот считается предварительным.
const ConfidentPrints = 6

// Volume — суммарный объём серии в лотах: сколько робот уже напечатал.
func (r Robot) Volume() float64 { return r.QtyTypical * float64(r.Prints) }

// NextBeatAfter — когда робот ударит в следующий раз после момента t.
//
// Считается продолжением фазы серии вперёд, а не «последний принт плюс период»:
// публичная лента ISS запаздывает на минуты, и к моменту вопроса робот успевает
// отработать несколько тактов, которых мы ещё не видели. Период известен с
// точностью до сотых долей секунды, поэтому фаза переносится вперёд без накопления
// заметной ошибки.
func (r Robot) NextBeatAfter(t time.Time) time.Time {
	if r.PeriodSec <= 0 || r.LastSeen.IsZero() {
		return time.Time{}
	}
	period := time.Duration(r.PeriodSec * float64(time.Second))
	if period <= 0 {
		return time.Time{}
	}
	elapsed := t.Sub(r.LastSeen)
	beats := math.Floor(float64(elapsed)/float64(period)) + 1
	if beats < 1 {
		beats = 1
	}
	return r.LastSeen.Add(time.Duration(beats * float64(period)))
}

// Config — пороги детектора. Значения по умолчанию даёт DefaultConfig.
type Config struct {
	// Window — сколько последней ленты анализируем.
	Window time.Duration

	// MinPrints/MinBeats — минимум принтов в кластере одной лотовки и минимум
	// интервалов, легших в период. Это пороги для акций и товарных фьючерсов.
	MinPrints int
	MinBeats  int

	// MinPrintsCurrency/MinBeatsCurrency — то же для валютных инструментов, где
	// робот заявляется со второго повторяющегося принта: лента реже, случайных
	// совпадений размера меньше.
	//
	// На таком пороге серия — это один интервал, и статистические фильтры
	// (разброс, доля совпавших интервалов) вырождаются: проверять period нечем,
	// кроме того, что он попал в границы. Отсюда два следствия — такие находки
	// помечаются Provisional, а отсев ложных ложится на правило пропущенных
	// тактов в реестре сессий.
	MinPrintsCurrency int
	MinBeatsCurrency  int

	// QtyTolRel/QtyTolAbs — допуск на «одинаковый размер»: робот дробит заявку и
	// печатает не строго одну и ту же лотовку. Допуск абсолютный, ±1 лот:
	// относительный на крупной лотовке (±1% от 3000 — это ±30 лотов) склеивал
	// в одного робота заведомо разные серии.
	QtyTolRel float64
	QtyTolAbs float64

	// MinPeriod/MaxPeriod — границы правдоподобного тайминга. Нижняя граница не
	// может быть меньше пары секунд: биржевая метка времени дискретна по секунде.
	MinPeriod time.Duration
	MaxPeriod time.Duration

	// PeriodTolAbs/PeriodTolRel — допуск на отдельный интервал. Абсолютная часть
	// покрывает секундное округление TRADETIME: при периоде 11.2 с лента отдаёт
	// интервалы то 11, то 12 секунд, и это один и тот же робот. Относительная часть
	// намеренно мала: на длинном периоде щедрый допуск позволяет подогнать «период»
	// под любые случайные сделки, и лента начинает кишеть несуществующими роботами.
	PeriodTolAbs time.Duration
	PeriodTolRel float64

	// MaxSkip — сколько тактов подряд робот может промолчать (не нашёл ликвидности),
	// чтобы серия всё ещё считалась непрерывной.
	MaxSkip int

	// MinUnitBeatRatio — какая доля совпавших интервалов обязана быть ровно в один
	// такт. Без этого условия детектор набирает «роботов» из редких случайных сделок,
	// раскладывая их по кратным тактам: настоящий робот пропуски делает изредка.
	MinUnitBeatRatio float64

	// MaxJitter — максимальный коэффициент вариации интервалов. Главный фильтр
	// против случайного потока: у пуассоновской ленты CV ≈ 1, у робота ≈ 0.
	MaxJitter float64

	// MinMatchRatio — какая доля интервалов серии обязана лечь в период.
	MinMatchRatio float64
}

// DefaultConfig — пороги, подобранные под ленту MOEX с секундной меткой времени.
func DefaultConfig() Config {
	return Config{
		Window:            20 * time.Minute,
		MinPrints:         3,
		MinBeats:          2,
		MinPrintsCurrency: 2,
		MinBeatsCurrency:  1,
		QtyTolRel:         0,
		QtyTolAbs:         1,
		MinPeriod:         2 * time.Second,
		MaxPeriod:         5 * time.Minute,
		PeriodTolAbs:      1200 * time.Millisecond,
		PeriodTolRel:      0.02,
		MaxSkip:           3,
		MaxJitter:         0.15,
		MinMatchRatio:     0.6,
		MinUnitBeatRatio:  0.5,
	}
}
