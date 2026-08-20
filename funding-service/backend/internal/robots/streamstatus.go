package robots

import "time"

// lagAlpha — вес свежего замера в скользящем среднем отставания. Отдельные принты
// приходят пачками и рвано, поэтому в отчёт идёт сглаженная величина, а не последняя.
const lagAlpha = 0.1

// lagSane — потолок разумного отставания принта. Всё, что больше, — не измерение
// скорости, а хвост, который источник досылает после переподключения; такие
// значения в среднее не пускаем, иначе один обрыв надолго испортит цифру.
const lagSane = 5 * time.Minute

// StreamStatus — чем сейчас живёт лента принтов.
//
// Нужен странице: она обязана честно сказать, на что смотрит пользователь.
// Поток брокера приносит сделку через секунду-другую, публичный фид MOEX ISS —
// ровно через пятнадцать минут, и «время до удара» на этих двух источниках
// означает разное: в первом случае почти наблюдение, во втором — экстраполяция.
type StreamStatus struct {
	// Enabled — быстрый источник настроен (есть токен брокера).
	Enabled bool `json:"enabled"`
	// Connected — соединение с ним держится прямо сейчас.
	Connected bool `json:"connected"`
	// Symbols — сколько инструментов покрыто потоком. Остальные идут по ISS.
	Symbols int `json:"symbols"`
	// LastPrintAt — биржевое время последнего принта из потока. Пустое —
	// подписка есть, но сделок по ней ещё не было (например, до открытия торгов).
	LastPrintAt time.Time `json:"last_print_at,omitempty"`
	// LagMs — замеренное отставание потока: сколько проходит от биржевой метки
	// сделки до её появления у нас, миллисекунды. Ноль — ещё не мерили.
	LagMs int64 `json:"lag_ms"`
}

// StreamStatus — срез состояния быстрого источника для API.
func (c *Collector) StreamStatus() StreamStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.stream
	st.Enabled = c.opts.Stream != nil
	return st
}

func (c *Collector) setStreamSymbols(n int) {
	c.mu.Lock()
	c.stream.Symbols = n
	c.mu.Unlock()
}

func (c *Collector) setStreamConnected(on bool) {
	c.mu.Lock()
	c.stream.Connected = on
	c.mu.Unlock()
}

// noteStreamPrint запоминает свежесть потока. Вызывается уже под замком коллектора.
func (c *Collector) noteStreamPrint(printedAt time.Time) {
	if printedAt.After(c.stream.LastPrintAt) {
		c.stream.LastPrintAt = printedAt
	}
	lag := c.now().Sub(printedAt)
	if lag < 0 || lag > lagSane {
		return
	}
	ms := float64(lag.Milliseconds())
	if c.stream.LagMs == 0 {
		c.stream.LagMs = int64(ms)
		return
	}
	c.stream.LagMs = int64(float64(c.stream.LagMs)*(1-lagAlpha) + ms*lagAlpha)
}
