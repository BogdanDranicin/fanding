package robots

import (
	"context"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/source/moexiss"
)

// issClient — то, что коллектору нужно от клиента MOEX ISS; *moexiss.Client его
// удовлетворяет. Сужено до интерфейса, чтобы коллектор тестировался без сети.
type issClient interface {
	FetchTradeTail(ctx context.Context, feed moexiss.TradeFeed, pages int) ([]moexiss.Trade, error)
	FetchTradesOn(ctx context.Context, feed moexiss.TradeFeed, since int64) ([]moexiss.Trade, error)
	FetchBoardSecurities(ctx context.Context, feed moexiss.TradeFeed) ([]moexiss.Security, error)
}

// Store — то, что коллектору нужно от базы; *storage.Store его удовлетворяет.
type Store interface {
	UpsertRobot(ctx context.Context, in RobotRow) (int64, error)
}

// TradeStream — быстрый источник принтов, работающий постоянным потоком.
//
// Нужен потому, что публичная лента MOEX ISS приходит с задержкой ровно в
// пятнадцать минут, а поиску роботов важна свежесть принта. Реализация живёт в
// internal/source/tinvest; здесь только интерфейс, чтобы коллектор тестировался
// без сети и не зависел от конкретного брокера.
type TradeStream interface {
	// Symbols — инструменты, доступные источнику.
	Symbols(ctx context.Context) ([]StreamInstrument, error)
	// Run ведёт поток, пока не отменят контекст или не оборвётся соединение.
	// Возврат означает обрыв: коллектор подождёт и попробует снова, а лента тем
	// временем продолжит идти из ISS.
	Run(ctx context.Context, symbols []string, out chan<- Print) error
}

// StreamInstrument — инструмент из каталога быстрого источника.
//
// Режим торгов обязателен, а не для красоты: правила отбора писались под ленту
// ISS, где адрес уже сужен бордом, и «берём все акции» там означает именно акции
// основного режима. Каталог брокера ничем не сужен — в нём и иностранные бумаги,
// и весь товарный срочный рынок, — поэтому инструмент сначала сопоставляется с
// лентой, для которой правило написано.
type StreamInstrument struct {
	Symbol string
	// Board — режим торгов: TQBR у акций основного режима, SPBFUT у срочного.
	Board string
}

// CollectorOptions — настройки сбора.
type CollectorOptions struct {
	// Tapes — ленты рынков, которые опрашиваем целиком.
	Tapes []MarketTape
	// Watch — какие инструменты из этих лент берём в работу.
	Watch *Watchlist
	// Stream — быстрый источник принтов. nil означает работу только по ISS.
	Stream TradeStream
	// StreamRetry — пауза перед повторным подключением после обрыва потока.
	// Функция, а не число, чтобы задержка могла расти от попытки к попытке.
	StreamRetry func(attempt int) time.Duration
	// PollInterval — как часто опрашиваем ленту каждого тикера. На точность
	// тайминга не влияет: период считается по биржевым меткам TRADETIME, а не по
	// моментам опроса. Влияет только на то, как быстро робот появится на странице.
	PollInterval time.Duration
	// ScanInterval — как часто прогоняем детектор по накопленным лентам.
	ScanInterval time.Duration
	// StaleAfter — сколько робот может молчать, прежде чем считаться остановившимся.
	StaleAfter time.Duration
	// KeepClosed — сколько закрытые сессии висят в памяти для страницы.
	KeepClosed time.Duration
	Detector   Config
}

// DefaultCollectorOptions — рабочие значения по умолчанию.
func DefaultCollectorOptions() CollectorOptions {
	watch, _ := NewWatchlist("")
	return CollectorOptions{
		Tapes:        DefaultTapes(),
		Watch:        watch,
		PollInterval: 3 * time.Second,
		ScanInterval: 15 * time.Second,
		StaleAfter:   3 * time.Minute,
		KeepClosed:   12 * time.Hour,
		Detector:     DefaultConfig(),
	}
}

// reseedAfter — если по ленте столько времени нет ни одной новой сделки, курсор
// TRADENO пересеивается с хвоста. Так переживается смена торгового дня, после
// которой старый курсор больше не догоняет ленту.
const reseedAfter = 10 * time.Minute

// maxSessions ограничивает реестр, чтобы память не росла на длинной сессии.
const maxSessions = 500

// streamBuffer — запас в канале принтов быстрого источника. На плотной ленте
// сделки идут пачками, и разбор не должен подпирать чтение из сети.
const streamBuffer = 8192

// seedPages — сколько страниц ленты (по 5000 сделок) пробуем снять при старте.
//
// Одна страница ленты всего рынка акций накрывает около минуты торгов, так что
// двадцатиминутное окно затравкой целиком не закрыть: ISS отдаёт хвост лишь на
// несколько страниц вглубь, дальше идут пустые ответы. Это не беда — окно
// дозаполняется инкрементальным опросом за первые двадцать минут работы. Просить
// больше, чем биржа отдаёт, смысла нет: FetchTradeTail оборвётся на первой же
// неудачной странице.
const seedPages = 6

// Collector опрашивает ленты сделок, кормит ими детектор и держит текущий срез
// найденных роботов. Один экземпляр обслуживает все тикеры сразу.
type Collector struct {
	client issClient
	store  Store
	opts   CollectorOptions
	log    zerolog.Logger
	now    func() time.Time

	mu  sync.Mutex
	det *Detector
	reg *registry
	day *dayVolumes
	// hour — оборот инструментов за последний час: знаменатель силы робота.
	hour *hourVolumes
	// streamHead — до какого биржевого времени ленту каждого инструмента закрыл
	// быстрый источник. Принты ISS не новее этой метки отбрасываются: их та же
	// сделка, только пришедшая на пятнадцать минут позже.
	//
	// Метка по инструменту, а не общая: поток покрывает не всё, чем торгует биржа,
	// и по бумаге вне подписки ISS обязан остаться единственным источником. Если
	// поток обрывается, метка замирает — и ISS сам собой подхватывает всё, что
	// после неё, без переключения режимов.
	streamHead map[string]time.Time
	// stream — что происходит с быстрым источником прямо сейчас. Нужно странице:
	// от того, каким источником пришёл принт, зависит его свежесть, а разница
	// между потоком брокера и публичным фидом ISS — пятнадцать минут.
	stream StreamStatus
	// streamCovers — инструменты, на которые подписан быстрый источник. По нему
	// страница отвечает на вопрос «почему вот эта бумага отстаёт»: всё, чего
	// здесь нет, идёт лентой ISS и приходит на пятнадцать минут позже.
	streamCovers map[string]bool
}

// NewCollector собирает коллектор поверх живого клиента ISS. store может быть nil —
// тогда роботы живут только в памяти и страница истории покажет текущую сессию сервиса.
func NewCollector(client issClient, store Store, opts CollectorOptions, log zerolog.Logger) *Collector {
	if opts.Watch == nil {
		opts.Watch = &Watchlist{}
	}
	det := NewDetector(opts.Detector)
	return &Collector{
		client: client,
		store:  store,
		opts:   opts,
		log:    log.With().Str("component", "robots").Logger(),
		now:    time.Now,
		det:    det,
		// Чёрный список снятых серий держим на длину окна анализа: ровно столько
		// снятая серия ещё видна детектору.
		reg:          newRegistry(opts.StaleAfter, opts.KeepClosed, opts.Detector.Window, maxSessions, det.BeatTol),
		day:          newDayVolumes(),
		hour:         newHourVolumes(),
		streamHead:   make(map[string]time.Time),
		streamCovers: make(map[string]bool),
	}
}

// universeInterval — как часто перечитывается справочник инструментов режима
// акций. Состав режима меняется листингами, то есть раз в недели, — суток хватает
// с запасом; чаще незачем.
const universeInterval = 24 * time.Hour

// Run ведёт сбор до отмены контекста: горутина на ленту плюс общий цикл сканирования.
func (c *Collector) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.universeLoop(ctx)
	}()
	for _, t := range c.opts.Tapes {
		wg.Add(1)
		go func(tape MarketTape) {
			defer wg.Done()
			c.pollTape(ctx, tape)
		}(t)
	}
	if c.opts.Stream != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.streamLoop(ctx)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.scanLoop(ctx)
	}()
	wg.Wait()
}

// Snapshot — текущий срез роботов для API. Поля, зависящие от момента запроса
// (время следующего удара, сила относительно часового оборота), считаются здесь.
func (c *Collector) Snapshot() []Session {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.reg.snapshot()
	for i := range out {
		// Час оборота отсчитывается от последнего принта робота, а не от стенных
		// часов: у бумаги, идущей только по ISS, лента отстаёт на пятнадцать минут,
		// и окно от «сейчас» было бы пустым в своей свежей четверти.
		out[i].fill(now,
			c.day.get(out[i].Symbol, out[i].LastSeen),
			c.hour.get(out[i].Symbol, out[i].LastSeen))
	}
	return out
}

// DayVolumes — дневной оборот по каждой ленте, база для оценки силы роботов.
func (c *Collector) DayVolumes() []DayVolume {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.day.snapshot()
}

// Tapes — ленты, которые опрашиваем (для диагностики на странице).
func (c *Collector) Tapes() []MarketTape { return c.opts.Tapes }

// WatchDescription — чем ограничен отбор инструментов.
func (c *Collector) WatchDescription() string { return c.opts.Watch.Describe() }

// Symbols — инструменты, по которым сейчас есть лента. Именно они, а не список
// правил, наполняют выбор тикера на странице.
func (c *Collector) Symbols() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.det.Symbols()
}

// universeLoop держит справочник акций свежим.
//
// Пока справочник не прочитан, лента режима идёт целиком — так же, как работало
// раньше. Это важнее аккуратности: сеть может лежать, а сбор должен идти.
func (c *Collector) universeLoop(ctx context.Context) {
	for {
		c.refreshUniverse(ctx)
		if !sleepCtx(ctx, universeInterval) {
			return
		}
	}
}

func (c *Collector) refreshUniverse(ctx context.Context) {
	for _, tape := range c.opts.Tapes {
		if tape.Engine != stockEngine || tape.Board == "" {
			continue
		}
		list, err := c.client.FetchBoardSecurities(ctx, moexiss.TradeFeed{
			Engine: tape.Engine, Market: tape.Market, Board: tape.Board,
		})
		if err != nil {
			c.log.Warn().Err(err).Str("tape", tape.Name).
				Msg("robots: справочник инструментов не прочитан, слежу за всем режимом")
			continue
		}
		shares := make(map[string]bool, len(list))
		for _, sec := range list {
			if KeepSecurity(sec.SecType) {
				shares[sec.SecID] = true
			}
		}
		c.opts.Watch.SetStockUniverse(shares)
		c.log.Info().Str("tape", tape.Name).
			Int("securities", len(list)).Int("shares", len(shares)).
			Msg("robots: справочник инструментов обновлён")
	}
}

// pollTape тянет ленту рынка инкрементально по курсору TRADENO.
func (c *Collector) pollTape(ctx context.Context, tape MarketTape) {
	tf := moexiss.TradeFeed{Engine: tape.Engine, Market: tape.Market, Board: tape.Board}
	log := c.log.With().Str("tape", tape.Name).Logger()

	// Стартовое смещение разводит запросы по лентам во времени: иначе они уходят
	// в ISS одной пачкой каждые PollInterval.
	if !sleepCtx(ctx, time.Duration(rand.Int63n(int64(c.opts.PollInterval)+1))) {
		return
	}

	var cursor int64
	lastNew := c.now()

	for {
		var (
			trades []moexiss.Trade
			err    error
		)
		if cursor == 0 {
			// В ленту входим с хвоста: бэкфилл всей сессии — это миллионы сделок,
			// а детектору нужны последние минуты. Берём столько страниц, чтобы
			// затравка накрыла окно анализа сразу, а не через двадцать минут
			// работы сервиса.
			trades, err = c.client.FetchTradeTail(ctx, tf, seedPages)
		} else {
			trades, err = c.client.FetchTradesOn(ctx, tf, cursor)
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Debug().Err(err).Msg("robots: trades poll failed")
		}

		now := c.now()
		if len(trades) > 0 {
			cursor = trades[len(trades)-1].TradeNo
			lastNew = now
			c.ingest(tape, trades)
		} else if cursor != 0 && now.Sub(lastNew) > reseedAfter {
			// Курсор больше не догоняет ленту (обычно — сменился торговый день).
			log.Debug().Int64("cursor", cursor).Msg("robots: пересеиваю курсор с хвоста ленты")
			cursor = 0
			lastNew = now
		}

		if !sleepCtx(ctx, c.opts.PollInterval) {
			return
		}
	}
}

// streamLoop держит быстрый источник подключённым, переподключаясь после обрывов.
// Пока он работает, лента ISS остаётся подключённой и молча подавляется водяной
// меткой — так обрыв потока не оставляет сервис без данных вовсе.
func (c *Collector) streamLoop(ctx context.Context) {
	symbols, err := c.streamSymbols(ctx)
	if err != nil {
		c.log.Warn().Err(err).Msg("robots: не получил список инструментов быстрого источника, работаю по ISS")
		return
	}
	if len(symbols) == 0 {
		c.log.Warn().Msg("robots: быстрый источник не знает ни одного нужного инструмента, работаю по ISS")
		return
	}
	c.log.Info().Int("symbols", len(symbols)).Msg("robots: быстрый источник подключается")
	c.setStreamSymbols(symbols)

	for attempt := 0; ctx.Err() == nil; attempt++ {
		prints := make(chan Print, streamBuffer)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for p := range prints {
				c.ingestStream(p)
			}
		}()

		c.setStreamConnected(true)
		err := c.opts.Stream.Run(ctx, symbols, prints)
		c.setStreamConnected(false)
		close(prints)
		<-done

		if ctx.Err() != nil {
			return
		}
		delay := 5 * time.Second
		if c.opts.StreamRetry != nil {
			delay = c.opts.StreamRetry(attempt)
		}
		c.log.Warn().Err(err).Dur("retry_in", delay).Msg("robots: быстрый источник оборвался, лента идёт из ISS")
		if !sleepCtx(ctx, delay) {
			return
		}
	}
}

// streamSymbols — пересечение того, что умеет быстрый источник, с тем, за чем мы
// следим. Инструменты вне пересечения остаются на ISS.
func (c *Collector) streamSymbols(ctx context.Context) ([]string, error) {
	available, err := c.opts.Stream.Symbols(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(available))
	for _, in := range available {
		tape, ok := c.tapeForBoard(in.Board)
		if !ok {
			continue // режим торгов, за которым мы не следим
		}
		if c.opts.Watch.Keep(in.Symbol, tape) {
			out = append(out, in.Symbol)
		}
	}
	return out, nil
}

// tapeForBoard сопоставляет режим торгов инструмента с лентой рынка, по правилам
// которой этот инструмент отбирается.
func (c *Collector) tapeForBoard(board string) (MarketTape, bool) {
	for _, tape := range c.opts.Tapes {
		switch {
		case tape.Board != "" && tape.Board == board:
			return tape, true
		case tape.Engine == futEngine && board == fortsBoard:
			return tape, true
		}
	}
	return MarketTape{}, false
}

// ingestStream принимает принт быстрого источника и двигает водяную метку.
func (c *Collector) ingestStream(p Print) {
	if p.Symbol == "" || p.Qty <= 0 || p.Time.IsZero() {
		return
	}
	if p.Side != SideBuy && p.Side != SideSell {
		return
	}
	c.mu.Lock()
	if IsCurrencyTicker(p.Symbol) {
		c.det.MarkCurrency(p.Symbol)
	}
	c.det.Add(p)
	c.day.add(p)
	c.hour.add(p)
	if p.Time.After(c.streamHead[p.Symbol]) {
		c.streamHead[p.Symbol] = p.Time
	}
	c.noteStreamPrint(p.Time)
	c.mu.Unlock()
}

// ingest переводит сделки ленты рынка в принты и кладёт их в детектор, отбрасывая
// инструменты вне списка наблюдения.
func (c *Collector) ingest(tape MarketTape, trades []moexiss.Trade) {
	prints := make([]Print, 0, len(trades))
	currency := make([]string, 0, 8)
	seen := make(map[string]bool, 16)
	for _, t := range trades {
		// Адресные сделки идут мимо стакана и к роботам в ленте отношения не имеют;
		// сделки чужого дня биржа приписывает к текущей сессии и они ломают тайминг.
		if t.OffMarket || t.Backdated || t.Timestamp.IsZero() {
			continue
		}
		side := Side(t.Side)
		if side != SideBuy && side != SideSell {
			continue
		}
		if !c.opts.Watch.Keep(t.SecID, tape) {
			continue
		}
		// Тикер приводится к верхнему регистру: срочный рынок ISS пишет контракты
		// вперемешку («SiU6», «EuU6»), а быстрый источник — заглавными. Пока
		// регистры расходились, один и тот же контракт жил двумя лентами: водяная
		// метка чужую не накрывала, и ISS заводил на пятнадцать минут отставший
		// дубль каждого фьючерсного робота.
		symbol := strings.ToUpper(t.SecID)
		if !seen[symbol] {
			seen[symbol] = true
			if IsCurrencyTicker(symbol) {
				currency = append(currency, symbol)
			}
		}
		prints = append(prints, Print{
			TradeNo: t.TradeNo,
			Symbol:  symbol,
			Time:    t.Timestamp,
			Price:   t.Price,
			Qty:     t.Quantity,
			Side:    side,
		})
	}
	if len(prints) == 0 {
		return
	}
	c.mu.Lock()
	// Порог обнаружения зависит от инструмента, а какие инструменты вообще есть
	// в ленте, известно только по факту прихода сделок.
	c.det.MarkCurrency(currency...)
	kept := prints[:0]
	for _, p := range prints {
		// Ту же сделку быстрый источник принёс пятнадцатью минутами раньше.
		if head, ok := c.streamHead[p.Symbol]; ok && !p.Time.After(head) {
			continue
		}
		kept = append(kept, p)
	}
	c.det.Add(kept...)
	for _, p := range kept {
		c.day.add(p)
		c.hour.add(p)
	}
	// Режем ленту сразу, а не только на сканах: на плотной бумаге между сканами
	// набегают тысячи сделок, и лента упиралась бы в аварийный предел длины.
	c.det.Trim(c.now())
	c.mu.Unlock()
}

// scanLoop периодически прогоняет детектор и сохраняет изменения.
func (c *Collector) scanLoop(ctx context.Context) {
	ticker := time.NewTicker(c.opts.ScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.scanOnce(ctx)
		}
	}
}

// scanOnce — один проход детектора: находки сшиваются в сессии и уезжают в базу.
func (c *Collector) scanOnce(ctx context.Context) {
	now := c.now()

	c.mu.Lock()
	found := c.det.Scan(now)
	c.reg.observe(found, now, c.det.Heads(now))
	dirty := c.reg.takeDirty()
	rows := make([]RobotRow, 0, len(dirty))
	for _, s := range dirty {
		row := rowOf(*s)
		// Обороты бумаги записываем вместе со строкой: они живут только в памяти
		// сбора, и к моменту, когда историю откроют, их уже неоткуда взять.
		row.HourLots = c.hour.get(s.Symbol, s.LastSeen).Total()
		row.DaySideLots = c.day.get(s.Symbol, s.LastSeen).Side(s.Side)
		rows = append(rows, row)
	}
	c.mu.Unlock()

	if c.store == nil || len(rows) == 0 {
		return
	}
	ids := make([]int64, len(rows))
	for i, row := range rows {
		id, err := c.store.UpsertRobot(ctx, row)
		if err != nil {
			c.log.Warn().Err(err).Str("symbol", row.Symbol).Msg("robots: не сохранил робота")
			continue
		}
		ids[i] = id
	}

	// Идентификаторы новых строк возвращаем в реестр, чтобы следующее обновление
	// той же сессии не создало в базе вторую строку.
	c.mu.Lock()
	for i, s := range dirty {
		if s.ID == 0 && ids[i] != 0 {
			s.ID = ids[i]
		}
	}
	c.mu.Unlock()
}

// sleepCtx спит d и возвращает false, если контекст отменили раньше.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
