package robots

import "time"

// RobotRow — сессия робота в том виде, в каком она едет в базу и обратно.
// Держится здесь, а не в storage, чтобы коллектор не зависел от слоя хранения.
type RobotRow struct {
	ID         int64
	Symbol     string
	Side       string
	QtyMin     float64
	QtyMax     float64
	QtyTypical float64
	PeriodSec  float64
	Jitter     float64
	Prints     int
	Beats      int
	Confidence float64
	PriceFirst float64
	PriceLast  float64
	FirstSeen  time.Time
	LastSeen   time.Time
	DetectedAt time.Time
	UpdatedAt  time.Time
	Active     bool
	// HourLots и DaySideLots — знаменатели силы на момент сохранения. Часовой
	// оборот живёт только в памяти сбора, и без записи история показывала бы по
	// графе «сила» один прочерк: величина была известна, её просто теряли.
	HourLots    float64
	DaySideLots float64
}

// rowOf переводит сессию в строку базы.
func rowOf(s Session) RobotRow {
	return RobotRow{
		ID:         s.ID,
		Symbol:     s.Symbol,
		Side:       string(s.Side),
		QtyMin:     s.QtyMin,
		QtyMax:     s.QtyMax,
		QtyTypical: s.QtyTypical,
		PeriodSec:  s.PeriodSec,
		Jitter:     s.Jitter,
		Prints:     s.Prints,
		Beats:      s.Beats,
		Confidence: s.Confidence,
		PriceFirst: s.PriceFirst,
		PriceLast:  s.PriceLast,
		FirstSeen:  s.FirstSeen,
		LastSeen:   s.LastSeen,
		DetectedAt: s.DetectedAt,
		UpdatedAt:  s.UpdatedAt,
		Active:     s.Active,
		// Заполняются вызывающим: сама сессия про обороты бумаги не знает,
		// их держит коллектор.
	}
}

// SessionOf переводит строку базы обратно в сессию — так страница истории отдаёт
// сохранённых роботов в том же виде, что и живой срез коллектора.
func SessionOf(r RobotRow) Session {
	s := Session{
		ID: r.ID,
		Robot: Robot{
			Symbol:     r.Symbol,
			Side:       Side(r.Side),
			QtyMin:     r.QtyMin,
			QtyMax:     r.QtyMax,
			QtyTypical: r.QtyTypical,
			PeriodSec:  r.PeriodSec,
			Jitter:     r.Jitter,
			Prints:     r.Prints,
			Beats:      r.Beats,
			Confidence: r.Confidence,
			PriceFirst: r.PriceFirst,
			PriceLast:  r.PriceLast,
			FirstSeen:  r.FirstSeen,
			LastSeen:   r.LastSeen,
		},
		DetectedAt: r.DetectedAt,
		UpdatedAt:  r.UpdatedAt,
		Active:     r.Active,
	}
	// Производные величины считаются здесь же, чтобы строка истории приезжала на
	// страницу в том же виде, что и живая: страница не должна знать, из какого
	// источника пришла сессия, и уж тем более гасить графы «в истории их не бывает».
	s.PrintLots = r.QtyTypical
	s.VolumeLots = s.Volume()
	s.HourLots = r.HourLots
	s.DaySideLots = r.DaySideLots
	s.LotsPerMin = s.lotsPerMin()
	s.StrengthPct = strengthPct(s.LotsPerMin, s.HourLots)
	return s
}
