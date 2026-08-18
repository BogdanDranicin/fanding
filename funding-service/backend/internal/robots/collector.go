package robots

import (
	"context"
	"math/rand"
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
}

// Store — то, что коллектору нужно от базы; *storage.Store его удовлетворяет.
type Store interface {
	UpsertRobot(ctx context.Context, in RobotRow) (int64, error)
}

// CollectorOptions — настройки сбора.
type CollectorOptions struct {
	Feeds []Feed
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
	return CollectorOptions{
		Feeds:        DefaultFeeds(),
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

// seedPages — сколько страниц ленты (по 5000 сделок) снимаем при входе в тикер.
// Две страницы накрывают окно анализа даже на самых плотных бумагах.
const seedPages = 2

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
}

// NewCollector собирает коллектор поверх живого клиента ISS. store может быть nil —
// тогда роботы живут только в памяти и страница истории покажет текущую сессию сервиса.
func NewCollector(client issClient, store Store, opts CollectorOptions, log zerolog.Logger) *Collector {
	det := NewDetector(opts.Detector)
	for _, f := range opts.Feeds {
		if f.Currency {
			det.MarkCurrency(f.Symbol)
		}
	}
	return &Collector{
		client: client,
		store:  store,
		opts:   opts,
		log:    log.With().Str("component", "robots").Logger(),
		now:    time.Now,
		det:    det,
		// Чёрный список снятых серий держим на длину окна анализа: ровно столько
		// снятая серия ещё видна детектору.
		reg: newRegistry(opts.StaleAfter, opts.KeepClosed, opts.Detector.Window, maxSessions, det.BeatTol),
		day: newDayVolumes(),
	}
}

// Run ведёт сбор до отмены контекста: горутина на ленту плюс общий цикл сканирования.
func (c *Collector) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, f := range c.opts.Feeds {
		wg.Add(1)
		go func(feed Feed) {
			defer wg.Done()
			c.pollFeed(ctx, feed)
		}(f)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.scanLoop(ctx)
	}()
	wg.Wait()
}

// Snapshot — текущий срез роботов для API. Поля, зависящие от момента запроса
// (время следующего удара, сила относительно дневного оборота), считаются здесь.
func (c *Collector) Snapshot() []Session {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.reg.snapshot()
	for i := range out {
		out[i].fill(now, c.day.get(out[i].Symbol, out[i].LastSeen))
	}
	return out
}

// DayVolumes — дневной оборот по каждой ленте, база для оценки силы роботов.
func (c *Collector) DayVolumes() []DayVolume {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.day.snapshot()
}

// Feeds — за какими тикерами следим (для диагностики на странице).
func (c *Collector) Feeds() []Feed { return c.opts.Feeds }

// pollFeed тянет ленту одного тикера инкрементально по курсору TRADENO.
func (c *Collector) pollFeed(ctx context.Context, feed Feed) {
	tf := moexiss.TradeFeed{Engine: feed.Engine, Market: feed.Market, Board: feed.Board, SecID: feed.SecID}
	log := c.log.With().Str("symbol", feed.Symbol).Logger()

	// Стартовое смещение разводит запросы по тикерам во времени: иначе все ленты
	// уходят в ISS одной пачкой каждые PollInterval.
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
			// В ленту входим с хвоста: бэкфилл всей сессии по ликвидной бумаге —
			// это сотни тысяч сделок, а детектору нужны последние минуты. Берём
			// столько страниц, чтобы затравка накрыла окно анализа сразу, а не
			// через двадцать минут работы сервиса.
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
			c.ingest(feed.Symbol, trades)
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

// ingest переводит сделки ISS в принты и кладёт их в детектор.
func (c *Collector) ingest(symbol string, trades []moexiss.Trade) {
	prints := make([]Print, 0, len(trades))
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
	c.det.Add(prints...)
	for _, p := range prints {
		c.day.add(p)
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
		rows = append(rows, rowOf(*s))
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
