package telegram

import (
	"context"
	"fmt"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/funding"
)

const sendRateLimit = 40 * time.Millisecond // 25 msg/sec max

// Dispatcher listens to OnNewPublication and sends Telegram alerts.
type Dispatcher struct {
	api        *tgbotapi.BotAPI
	pool       *pgxpool.Pool
	snapshotFn func() funding.FundingSnapshot
	log        zerolog.Logger
}

// NewDispatcher creates a Dispatcher using the bot's API handle.
func NewDispatcher(bot *Bot, pool *pgxpool.Pool, snapshotFn func() funding.FundingSnapshot, log zerolog.Logger) *Dispatcher {
	return &Dispatcher{
		api:        bot.api,
		pool:       pool,
		snapshotFn: snapshotFn,
		log:        log,
	}
}

// Run blocks, forwarding each publication signal to all linked users.
func (d *Dispatcher) Run(ctx context.Context, pubCh <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case pubTime, ok := <-pubCh:
			if !ok {
				return
			}
			snap := d.snapshotFn()
			text := formatAlert(pubTime, snap)
			d.broadcast(ctx, text)
		}
	}
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

	d.log.Info().Int("recipients", len(chatIDs)).Msg("publication alert sent")
}

func formatAlert(pubTime time.Time, snap funding.FundingSnapshot) string {
	date := pubTime.Format("2006-01-02")

	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>НОВЫЕ ДАННЫЕ:</b>\nДата: %s\n", date)

	usdRate := snap.USDRUBF.OfficialRate
	eurRate := snap.EURRUBF.OfficialRate

	if usdRate != nil || eurRate != nil {
		sb.WriteString("\n<b>Межбанк:</b>\n")
		if usdRate != nil {
			fmt.Fprintf(&sb, "Курс USD %.4f\n", *usdRate)
		}
		if eurRate != nil {
			fmt.Fprintf(&sb, "Курс EUR %.4f\n", *eurRate)
		}
	}

	usdFund := snap.USDRUBF.CBFunding
	eurFund := snap.EURRUBF.CBFunding
	cnyFund := snap.CNYRUBF.MOEXFunding

	if usdFund != nil || eurFund != nil || cnyFund != nil {
		sb.WriteString("\n<b>Фандинги:</b>\n")
		if usdFund != nil {
			fmt.Fprintf(&sb, "USDRUBF: %+.6f\n", *usdFund)
		}
		if eurFund != nil {
			fmt.Fprintf(&sb, "EURRUBF: %+.6f\n", *eurFund)
		}
		if cnyFund != nil {
			fmt.Fprintf(&sb, "CNYRUBF: %+.6f\n", *cnyFund)
		}
	}

	return sb.String()
}
