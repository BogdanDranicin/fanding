package robots

import (
	"sort"
	"strings"
	"time"
)

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
	// Missing — инструменты, за которыми мы следим, но которых нет в каталоге
	// брокера: только по ним лента и остаётся пятнадцатиминутной. Список, а не
	// счётчик, потому что вопрос «откуда задержка» задаётся про конкретную
	// бумагу, и ответ «у этих двух» кончает разговор, а «у скольких-то» — нет.
	Missing []string `json:"missing,omitempty"`
}

// StreamStatus — срез состояния быстрого источника для API.
func (c *Collector) StreamStatus() StreamStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.stream
	st.Enabled = c.opts.Stream != nil
	st.Missing = c.uncoveredLocked()
	return st
}

// uncoveredLocked — за какими инструментами мы следим мимо быстрого источника.
// Считается по тем, чья лента реально идёт: список правил отбора тут не годится,
// он описывает намерение, а не факт. Must be called while holding c.mu.
func (c *Collector) uncoveredLocked() []string {
	if len(c.streamCovers) == 0 {
		return nil
	}
	var out []string
	for _, sym := range c.det.Symbols() {
		if !c.streamCovers[sym] {
			out = append(out, sym)
		}
	}
	sort.Strings(out)
	return out
}

func (c *Collector) setStreamSymbols(symbols []string) {
	c.mu.Lock()
	c.stream.Symbols = len(symbols)
	c.streamCovers = make(map[string]bool, len(symbols))
	for _, sym := range symbols {
		c.streamCovers[strings.ToUpper(sym)] = true
	}
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
