package telegram

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/source/cbr"
)

const (
	sendRateLimit = 40 * time.Millisecond // 25 msg/sec max
	// sendWorkers — сколько отправок идёт одновременно. Каждый Send — это HTTPS-запрос
	// через прокси (200–500 мс), поэтому последовательная рассылка добавляла подписчику
	// №N почти N × RTT задержки. Параллелим, а темп раздачи держим на sendRateLimit.
	sendWorkers = 8
)

// sender — то, что умеет отправлять сообщение в Telegram (*tgbotapi.BotAPI в проде).
// Вынесено в интерфейс, чтобы рассылку можно было проверить в тестах без сети.
type sender interface {
	Send(c tgbotapi.Chattable) (tgbotapi.Message, error)
}

// Dispatcher listens to settlement and publication signals and sends Telegram alerts.
type Dispatcher struct {
	api        sender
	pool       *pgxpool.Pool
	snapshotFn func() funding.FundingSnapshot
	pubInfoFn  func() cbr.PublicationInfo
	log        zerolog.Logger
}

// NewDispatcher creates a Dispatcher using the bot's API handle. pubInfoFn is the
// CBR source's LastPublicationInfo: the published rates are taken from it, NOT from
// the engine snapshot — the KindNewOfficialRate ticks reach the engine asynchronously,
// so at signal time the snapshot may still hold yesterday's rates (observed 16.07).
func NewDispatcher(bot *Bot, pool *pgxpool.Pool, snapshotFn func() funding.FundingSnapshot, pubInfoFn func() cbr.PublicationInfo, log zerolog.Logger) *Dispatcher {
	return &Dispatcher{
		api:        bot.api,
		pool:       pool,
		snapshotFn: snapshotFn,
		pubInfoFn:  pubInfoFn,
		log:        log,
	}
}

// Run blocks, forwarding publication signals to all linked users. Публикация курса
// ЦБ — единственный повод написать подписчику: клиринг больше не рассылает ни
// «прогнозный фандинг зафиксирован» (решение 17.07 — точные цифры приходят с
// публикацией ЦБ), ни служебное «Обновление сервиса / Сервис перезапущен»
// (убрано 06.08.2026 — приходило каждый день и ничего не сообщало по делу).
func (d *Dispatcher) Run(ctx context.Context, pubCh <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-pubCh:
			if !ok {
				return
			}
			signalAt := time.Now()
			info := d.pubInfoFn()

			// Публикация без курсов — не публикация. Без них в сообщении не остаётся
			// ничего, кроме заголовка и времени: строки фандинга тоже считаются от
			// курса ЦБ (fundingLine возвращает пустую строку при rate ≤ 0). Ровно
			// такое пустое «📢 Фандинг зафиксирован» пришло 12.08.2026 в 16:26:16.
			// Корень закрыт в источнике (cbr.hasMajors), это — второй заслон: что бы
			// ни случилось выше, содержательно пустое уведомление не уходит.
			if info.USD <= 0 && info.EUR <= 0 {
				d.log.Warn().
					Str("date", info.Date).
					Str("winner", info.Winner).
					Msg("alert skipped: публикация без курсов")
				continue
			}

			// Список получателей тянем ПАРАЛЛЕЛЬНО ожиданию пересчёта: поход в базу
			// (Postgres на Render — десятки мс, на холодном пуле больше) раньше стоял
			// последовательно ПОСЛЕ ожидания и целиком ложился в задержку первого
			// сообщения. К моменту готовности текста список уже готов.
			recipients := make(chan []int64, 1)
			go func() { recipients <- d.recipients(ctx) }()

			snap := d.awaitCBFunding(ctx, info)
			waitDur := time.Since(signalAt)
			text := formatCBRAlert(t, info, snap)
			d.broadcast(ctx, text, <-recipients, signalAt, waitDur)
		}
	}
}

// awaitCBFunding ждёт, пока движок съест тики новой публикации и пересчитает точный
// фандинг: сигнал OnNewPublication летит параллельно тикам, и мгновенный снапшот ещё
// содержит вчерашние курсы (наблюдалось 16.07: сообщение со старыми курсами и без
// USD/EUR фандингов). Возвращает снапшот, как только курсы в нём совпали с публикацией
// и CBFunding посчитан, либо последний снапшот по таймауту/отмене.
// Шаг опроса — 20 мс, а не 200: движок съедает тики публикации за десятки мс, но
// первая проверка почти всегда попадает ДО этого, и на грубом шаге сообщение ждало
// лишний тик впустую (сайт при этом обновлялся по своему WS-циклу в 250 мс и уходил
// вперёд). Snapshot() — это захват мьютекса и арифметика по мапам, 500 вызовов за
// 10 с таймаута ничего не стоят.
func (d *Dispatcher) awaitCBFunding(ctx context.Context, info cbr.PublicationInfo) funding.FundingSnapshot {
	const timeout = 10 * time.Second
	const step = 20 * time.Millisecond
	deadline := time.Now().Add(timeout)
	for {
		snap := d.snapshotFn()
		usdReady := info.USD <= 0 || (rateEq(snap.USDRUBF.OfficialRate, info.USD) && snap.USDRUBF.CBFunding != nil)
		eurReady := info.EUR <= 0 || (rateEq(snap.EURRUBF.OfficialRate, info.EUR) && snap.EURRUBF.CBFunding != nil)
		if (usdReady && eurReady) || time.Now().After(deadline) {
			return snap
		}
		select {
		case <-ctx.Done():
			return snap
		case <-time.After(step):
		}
	}
}

func rateEq(got *float64, want float64) bool {
	return got != nil && math.Abs(*got-want) < 1e-9
}

// recipients возвращает chat_id всех привязанных пользователей.
func (d *Dispatcher) recipients(ctx context.Context) []int64 {
	rows, err := d.pool.Query(ctx,
		`SELECT telegram_chat_id FROM users WHERE telegram_chat_id IS NOT NULL`)
	if err != nil {
		d.log.Warn().Err(err).Msg("dispatcher: query users failed")
		return nil
	}
	defer rows.Close()

	var chatIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			chatIDs = append(chatIDs, id)
		}
	}
	return chatIDs
}

// broadcast рассылает text всем получателям параллельно, до sendWorkers отправок
// одновременно. Раньше рассылка была строго последовательной, и подписчик ждал
// не только свой запрос к Telegram, но и все предыдущие: при N получателях
// задержка последнего росла как N × (RTT + пауза). Теперь по паузе sendRateLimit
// расходятся только ЗАДАНИЯ (чтобы не пробить лимит Telegram ~30 сообщений/с),
// а сами запросы летят внахлёст.
//
// signalAt/waitDur — только для лога задержки: видно, сколько ушло на ожидание
// пересчёта фандинга, а сколько на саму отправку.
func (d *Dispatcher) broadcast(ctx context.Context, text string, chatIDs []int64, signalAt time.Time, waitDur time.Duration) {
	if len(chatIDs) == 0 {
		d.log.Warn().Dur("wait_funding", waitDur).Msg("alert skipped: no recipients")
		return
	}

	workers := sendWorkers
	if len(chatIDs) < workers {
		workers = len(chatIDs)
	}

	var (
		jobs   = make(chan int64)
		wg     sync.WaitGroup
		failed atomic.Int64
		firstN atomic.Int64 // задержка первого доставленного сообщения, нс
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				msg := tgbotapi.NewMessage(id, text)
				msg.ParseMode = "HTML"
				if _, err := d.api.Send(msg); err != nil {
					failed.Add(1)
					d.log.Warn().Err(err).Int64("chat_id", id).Msg("dispatcher: send failed")
					continue
				}
				firstN.CompareAndSwap(0, int64(time.Since(signalAt)))
			}
		}()
	}

	func() {
		defer close(jobs)
		for i, id := range chatIDs {
			if i > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(sendRateLimit):
				}
			}
			select {
			case jobs <- id:
			case <-ctx.Done():
				return
			}
		}
	}()
	wg.Wait()

	// Warn, а не Info: рассылка — событие раз в сутки, а прод крутится с
	// LOG_LEVEL=warn (та же причина, по которой на Warn логируется сама
	// публикация, «cbr rates emitted»). На Info эта строка в проде не видна —
	// а именно по ней сверяется задержка доставки.
	d.log.Warn().
		Int("recipients", len(chatIDs)).
		Int64("failed", failed.Load()).
		Dur("wait_funding", waitDur).
		Dur("first_delivered", time.Duration(firstN.Load())).
		Dur("total", time.Since(signalAt)).
		Msg("alert sent")
}

// indicatorEmoji подбирает цветовой индикатор по величине фандинга в процентах
// от курса ЦБ: 🟢 ≥0.14 · 🟡 0.05…0.14 · ⚪️ ±0.05 · 🟠 −0.14…−0.05 · 🔴 ≤−0.14.
func indicatorEmoji(pct float64) string {
	switch {
	case pct >= 0.14:
		return "🟢"
	case pct >= 0.05:
		return "🟡"
	case pct > -0.05:
		return "⚪️"
	case pct > -0.14:
		return "🟠"
	default:
		return "🔴"
	}
}

// fundingLine строит одну строку вида «🟢USDRUBF: +0.11730».
// Процент из сообщения убран (18+ 27.07): в ленте нужна сама ставка, и ровно в том
// виде, в каком её публикует биржа (SWAPRATE, 5 знаков) — чтобы сверять один в один.
// Процент по-прежнему считается: по нему выбирается цветовой индикатор.
// Пустая строка, если фандинг ещё не посчитан или нет базы для процента.
func fundingLine(sym string, fund *float64, rate float64) string {
	if fund == nil || rate <= 0 {
		return ""
	}
	pct := *fund / rate * 100
	return fmt.Sprintf("%s%s: %+.5f\n", indicatorEmoji(pct), sym, *fund)
}

// formatCBRAlert строит сообщение о зафиксированном фандинге после публикации ЦБ.
// Курсы — из PublicationInfo (ответ канала-победителя, без гонки со снапшотом);
// фандинги только USD/EUR — наш CBFunding (CNY убран 18.07).
func formatCBRAlert(pubTime time.Time, info cbr.PublicationInfo, snap funding.FundingSnapshot) string {
	msk := time.FixedZone("MSK", 3*60*60)

	var sb strings.Builder
	fmt.Fprintf(&sb, "📢 <b>Фандинг зафиксирован</b>\n%s МСК\n", pubTime.In(msk).Format("15:04:05"))

	// CNY фандинг убран из уведомлений (18.07): показываем только USD/EUR (наш CBFunding).
	lines := fundingLine("USDRUBF", snap.USDRUBF.CBFunding, info.USD) +
		fundingLine("EURRUBF", snap.EURRUBF.CBFunding, info.EUR)
	if lines != "" {
		sb.WriteString("\n")
		sb.WriteString(lines)
	}

	if info.USD > 0 || info.EUR > 0 {
		fmt.Fprintf(&sb, "\nКурс ЦБ на %s:", info.Date)
		if info.USD > 0 {
			fmt.Fprintf(&sb, " USD %.2f", info.USD)
		}
		if info.EUR > 0 {
			fmt.Fprintf(&sb, " / EUR %.2f", info.EUR)
		}
		if info.CNY > 0 {
			fmt.Fprintf(&sb, " / CNY %.2f", info.CNY)
		}
	}

	return sb.String()
}
