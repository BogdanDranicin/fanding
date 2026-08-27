package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/config"
	"github.com/funding-service/backend/internal/source"
	"github.com/funding-service/backend/internal/source/tinvest"
)

// tinvestFunding — живой поток сделок брокера как источник рыночных данных для
// расчёта фандинга.
//
// Зачем он появился (28.08.2026). Нога фьючерса в формуле MOEX — это
// средневзвешенная цена безадресных сделок за 10:00–15:30 МСК. Всё это время
// движок собирал её из публичной ленты MOEX ISS, которая отдаёт сделку через
// пятнадцать минут после того, как она прошла. Из-за этого движок физически не
// мог знать в 15:30, кончилось ли окно: последние пятнадцать минут сделок ещё
// были в пути. Отсюда весь ворох подпорок — отсрочка до 17:00, «предварительная»
// заморозка, пометка «(уточняется)» в телеграме — и отсюда же расхождения с
// биржей на четвёртом-пятом знаке: 19.08.2026 EUR ушёл подписчикам как 0.03367
// вместо 0.02919, потому что окно оборвалось на 15:11.
//
// При этом ровно те же сделки уже приходили в этот же процесс мгновенно: поток
// брокера питал страницу «Роботы». Сверка с биржей за 27.08.2026:
//
//	USDRUBF  поток 499703 лотов / 47478 сделок — MOEX VOLUME 499703 / 47478
//	EURRUBF  поток  18188 лотов /  5323 сделки — MOEX VOLUME  18188 /  5323
//
// то есть поток — точная копия биржевой ленты, просто без пятнадцатиминутной
// задержки. Так что нога фьючерса считается по нему, а лента ISS остаётся
// независимой сверкой и запасным вариантом на случай обрыва подписки.
type tinvestFunding struct {
	client *tinvest.Client
	log    zerolog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
}

var _ source.MarketDataSource = (*tinvestFunding)(nil)

func newTInvestFunding(c *tinvest.Client, log zerolog.Logger) *tinvestFunding {
	return &tinvestFunding{client: c, log: log.With().Str("source", "tinvest-funding").Logger()}
}

// Name implements source.MarketDataSource.
func (s *tinvestFunding) Name() string { return "tinvest" }

// Close implements source.MarketDataSource. Соединение принадлежит вызывающему
// (его же использует сбор роботов), поэтому здесь только снимается подписка.
func (s *tinvestFunding) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	return nil
}

// tinvestFundingBuffer — запас канала тиков. USDRUBF даёт под пятьдесят тысяч
// сделок в день, но идут они пачками, и разбор не должен подпирать чтение из сети.
const tinvestFundingBuffer = 4096

// Subscribe implements source.MarketDataSource.
//
// Ошибку не возвращает никогда: подписка поднимается в фоне и переподключается
// сама. Источник вспомогательный — ISS остаётся, — и падать из-за него на старте
// сервису незачем.
func (s *tinvestFunding) Subscribe(ctx context.Context, symbols []string) (<-chan source.Tick, error) {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	ch := make(chan source.Tick, tinvestFundingBuffer)
	go func() {
		defer close(ch)
		s.run(ctx, symbols, ch)
	}()
	return ch, nil
}

// catalogRetry — пауза перед повторной попыткой прочитать каталог инструментов.
const catalogRetry = 30 * time.Second

// run держит подписку живой до отмены контекста.
func (s *tinvestFunding) run(ctx context.Context, symbols []string, out chan<- source.Tick) {
	want := make(map[string]string, len(symbols)) // тикер в верхнем регистре -> наш символ
	for _, sym := range symbols {
		want[strings.ToUpper(sym)] = sym
	}

	byUID, err := s.resolve(ctx, want)
	if err != nil {
		return
	}
	uids := make([]string, 0, len(byUID))
	for uid := range byUID {
		uids = append(uids, uid)
	}
	if len(uids) == 0 {
		s.log.Warn().Strs("symbols", symbols).
			Msg("ни один инструмент не найден в каталоге брокера — нога фьючерса останется на ленте ISS")
		return
	}
	s.log.Info().Int("instruments", len(uids)).
		Msg("живой поток сделок подключается: нога фьючерса считается по нему")

	for attempt := 0; ctx.Err() == nil; attempt++ {
		err := s.session(ctx, uids, byUID, symbols, out)
		if ctx.Err() != nil {
			return
		}
		delay := tinvest.ReconnectDelay(attempt)
		s.log.Warn().Err(err).Dur("retry_in", delay).
			Msg("живой поток сделок оборвался — до восстановления нога фьючерса считается по ленте ISS")
		if !sleepCtxFunding(ctx, delay) {
			return
		}
	}
}

// resolve тянет каталог брокера, пока не получится, и оставляет только нужные
// инструменты.
func (s *tinvestFunding) resolve(ctx context.Context, want map[string]string) (map[string]string, error) {
	for {
		list, err := s.client.Instruments(ctx)
		if err == nil {
			byUID := make(map[string]string, len(want))
			for _, in := range list {
				if sym, ok := want[strings.ToUpper(in.Ticker)]; ok && in.UID != "" {
					byUID[in.UID] = sym
				}
			}
			return byUID, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		s.log.Warn().Err(err).Dur("retry_in", catalogRetry).Msg("каталог инструментов брокера не прочитан")
		if !sleepCtxFunding(ctx, catalogRetry) {
			return nil, ctx.Err()
		}
	}
}

// session ведёт одну подписку от подключения до обрыва, обрамляя её служебными
// тиками. Именно по ним движок понимает, покрыт ли живым потоком весь интервал
// 10:00–15:30: подписка, поднявшаяся в полдень, сделки утра уже не увидит.
func (s *tinvestFunding) session(
	ctx context.Context, uids []string, byUID map[string]string,
	symbols []string, out chan<- source.Tick,
) error {
	trades := make(chan tinvest.Trade, tinvestFundingBuffer)
	errc := make(chan error, 1)
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		errc <- s.client.StreamTrades(streamCtx, uids, byUID, trades)
		close(trades)
	}()

	// Тик «поток поднялся» уходит ДО первой сделки: пока его нет, движок считает
	// живую ногу неполной и не станет по ней морозить.
	if !s.mark(ctx, out, symbols, source.KindStreamUp) {
		return ctx.Err()
	}
	// Тик «поток упал» уходит при любом выходе, включая отмену: незакрытая
	// подписка, о которой движок думает, что она жива, — худшее из состояний.
	defer s.mark(context.WithoutCancel(ctx), out, symbols, source.KindStreamDown)

	for {
		select {
		case tr, ok := <-trades:
			if !ok {
				return <-errc
			}
			if tr.Price <= 0 || tr.Qty <= 0 || tr.Time.IsZero() {
				continue
			}
			select {
			case out <- source.Tick{
				Symbol:    tr.Ticker,
				Price:     tr.Price,
				Volume:    tr.Qty,
				Kind:      source.KindTrade,
				Timestamp: tr.Time,
				Source:    s.Name(),
				Live:      true,
			}:
			case <-ctx.Done():
				return ctx.Err()
			}
		case err := <-errc:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// mark рассылает служебный тик по всем символам подписки. Возвращает false,
// если контекст отменили раньше, чем тик ушёл.
func (s *tinvestFunding) mark(ctx context.Context, out chan<- source.Tick, symbols []string, kind source.TickKind) bool {
	now := time.Now()
	for _, sym := range symbols {
		select {
		case out <- source.Tick{
			Symbol:    sym,
			Kind:      kind,
			Timestamp: now,
			Source:    s.Name(),
			Live:      true,
		}:
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
			// Потребитель встал намертво — служебный тик не стоит того, чтобы
			// держать на нём горутину потока.
			return false
		}
	}
	return true
}

func sleepCtxFunding(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// dialTInvest поднимает соединение с брокером — одно на весь сервис. Возвращает
// nil, если токена нет или подключиться не удалось: и поиск роботов, и расчёт
// фандинга умеют работать по одной ленте MOEX ISS, просто с её отставанием.
func dialTInvest(ctx context.Context, cfg *config.Config, log zerolog.Logger) *tinvest.Client {
	if cfg.TInvestToken == "" {
		log.Warn().Msg("TINVEST_TOKEN не задан: нога фьючерса считается по ленте ISS с отставанием ~15 минут")
		return nil
	}
	client, err := tinvest.Dial(ctx, tinvest.Config{
		Token:   cfg.TInvestToken,
		AppName: "funding-service",
	})
	if err != nil {
		log.Error().Err(err).Msg("живой поток сделок недоступен, работаю по ленте ISS")
		return nil
	}
	return client
}
