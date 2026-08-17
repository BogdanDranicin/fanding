// Package robots ищет в ленте сделок MOEX торговых роботов: повторяющиеся
// принты одного размера, идущие через равные промежутки времени.
//
// Робот описывается четырьмя наблюдаемыми величинами — тикер, направление
// (агрессор покупал или продавал), лотовка (размер принта) и тайминг (период
// повторения). Пример: по SBER каждые 11.2 с проходит покупка 3011–3013 лотов.
package robots

import "time"

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

	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`

	PriceFirst float64 `json:"price_first"`
	PriceLast  float64 `json:"price_last"`
}

// Volume — суммарный объём серии в лотах.
func (r Robot) Volume() float64 { return r.QtyTypical * float64(r.Prints) }

// Config — пороги детектора. Значения по умолчанию даёт DefaultConfig.
type Config struct {
	// Window — сколько последней ленты анализируем.
	Window time.Duration

	// MinPrints — минимум принтов в кластере одной лотовки.
	MinPrints int
	// MinBeats — минимум интервалов, легших в найденный период.
	MinBeats int

	// QtyTolRel/QtyTolAbs — допуск на «одинаковый размер»: робот дробит заявку и
	// печатает не строго одну и ту же лотовку (3011 против 3013).
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
		Window:           20 * time.Minute,
		MinPrints:        6,
		MinBeats:         5,
		QtyTolRel:        0.01,
		QtyTolAbs:        1,
		MinPeriod:        2 * time.Second,
		MaxPeriod:        5 * time.Minute,
		PeriodTolAbs:     1200 * time.Millisecond,
		PeriodTolRel:     0.02,
		MaxSkip:          3,
		MaxJitter:        0.15,
		MinMatchRatio:    0.6,
		MinUnitBeatRatio: 0.5,
	}
}
