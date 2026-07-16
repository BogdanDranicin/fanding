package telegram

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/source/cbr"
)

const sendRateLimit = 40 * time.Millisecond // 25 msg/sec max

// Dispatcher listens to settlement and publication signals and sends Telegram alerts.
type Dispatcher struct {
	api        *tgbotapi.BotAPI
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

// Run blocks, forwarding publication signals to all linked users. The settlement
// signal no longer produces a «прогнозный фандинг зафиксирован» message (решение
// 17.07 — точные цифры приходят с публикацией ЦБ); вне окна настоящего клиринга
// он остаётся служебным «Обновлением сервиса» (рестарт).
func (d *Dispatcher) Run(ctx context.Context, settlCh, pubCh <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-settlCh:
			if !ok {
				return
			}
			if !isSettlementTime(t) {
				d.broadcast(ctx, formatRestartNotice(t))
			}
		case t, ok := <-pubCh:
			if !ok {
				return
			}
			info := d.pubInfoFn()
			snap := d.awaitCBFunding(ctx, info)
			text := formatCBRAlert(t, info, snap)
			d.broadcast(ctx, text)
		}
	}
}

// awaitCBFunding ждёт, пока движок съест тики новой публикации и пересчитает точный
// фандинг: сигнал OnNewPublication летит параллельно тикам, и мгновенный снапшот ещё
// содержит вчерашние курсы (наблюдалось 16.07: сообщение со старыми курсами и без
// USD/EUR фандингов). Возвращает снапшот, как только курсы в нём совпали с публикацией
// и CBFunding посчитан, либо последний снапшот по таймауту/отмене.
func (d *Dispatcher) awaitCBFunding(ctx context.Context, info cbr.PublicationInfo) funding.FundingSnapshot {
	const timeout = 10 * time.Second
	const step = 200 * time.Millisecond
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

func (d *Dispatcher) broadcast(ctx context.Context, text string) {
	rows, err := d.pool.Query(ctx,
		`SELECT telegram_chat_id FROM users WHERE telegram_chat_id IS NOT NULL`)
	if err != nil {
		d.log.Warn().Err(err).Msg("dispatcher: query users failed")
		return
	}
	defer rows.Close()

	var chatIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			chatIDs = append(chatIDs, id)
		}
	}

	for _, id := range chatIDs {
		msg := tgbotapi.NewMessage(id, text)
		msg.ParseMode = "HTML"
		if _, err := d.api.Send(msg); err != nil {
			d.log.Warn().Err(err).Int64("chat_id", id).Msg("dispatcher: send failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(sendRateLimit):
		}
	}

	d.log.Info().Int("recipients", len(chatIDs)).Msg("alert sent")
}

// isSettlementTime reports whether t falls into the real settlement window
// (15:30–15:45 МСК): движок стреляет сигналом на первом же тике после 15:30,
// то есть в течение секунд. Всё вне окна — восстановление после рестарта.
func isSettlementTime(t time.Time) bool {
	msk := time.FixedZone("MSK", 3*60*60)
	h, m, _ := t.In(msk).Clock()
	return h == 15 && m >= 30 && m < 45
}

// formatRestartNotice строит служебное сообщение, когда settlement лишь
// восстановлен после перезапуска сервиса.
func formatRestartNotice(t time.Time) string {
	msk := time.FixedZone("MSK", 3*60*60)
	return fmt.Sprintf("🔄 <b>Обновление сервиса</b>\n%s МСК\nСервис перезапущен, расчётные данные восстановлены.",
		t.In(msk).Format("15:04:05"))
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

// fundingLine строит одну строку вида «🟢USDRUBF: +0.150% (+0.1173)».
// Пустая строка, если фандинг ещё не посчитан или нет базы для процента.
func fundingLine(sym string, fund *float64, rate float64) string {
	if fund == nil || rate <= 0 {
		return ""
	}
	pct := *fund / rate * 100
	return fmt.Sprintf("%s%s: %+.3f%% (%+.4f)\n", indicatorEmoji(pct), sym, pct, *fund)
}

// formatCBRAlert строит сообщение о зафиксированном фандинге после публикации ЦБ.
// Курсы — из PublicationInfo (ответ канала-победителя, без гонки со снапшотом);
// фандинги USD/EUR — наш CBFunding, CNY — SWAPRATE MOEX. Проценты — от нового курса.
func formatCBRAlert(pubTime time.Time, info cbr.PublicationInfo, snap funding.FundingSnapshot) string {
	msk := time.FixedZone("MSK", 3*60*60)

	var sb strings.Builder
	fmt.Fprintf(&sb, "📢 <b>Фандинг зафиксирован</b>\n%s МСК\n", pubTime.In(msk).Format("15:04:05"))

	lines := fundingLine("USDRUBF", snap.USDRUBF.CBFunding, info.USD) +
		fundingLine("EURRUBF", snap.EURRUBF.CBFunding, info.EUR) +
		fundingLine("CNYRUBF", snap.CNYRUBF.MOEXFunding, info.CNY)
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
