package telegram

import (
	"context"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// Bot wraps a Telegram bot and handles user registration via link tokens.
type Bot struct {
	api  *tgbotapi.BotAPI
	pool *pgxpool.Pool
	log  zerolog.Logger
}

// New creates a Bot. Returns an error if the token is empty or invalid.
func New(token string, pool *pgxpool.Pool, log zerolog.Logger) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, err
	}
	log.Info().Str("username", api.Self.UserName).Msg("telegram bot authorised")
	return &Bot{api: api, pool: pool, log: log}, nil
}

// Run starts long-polling and blocks until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	cfg := tgbotapi.NewUpdate(0)
	cfg.Timeout = 30
	updates := b.api.GetUpdatesChan(cfg)

	for {
		select {
		case <-ctx.Done():
			b.api.StopReceivingUpdates()
			return
		case upd, ok := <-updates:
			if !ok {
				return
			}
			if upd.Message == nil || !upd.Message.IsCommand() {
				continue
			}
			b.handle(ctx, upd.Message)
		}
	}
}

func (b *Bot) handle(ctx context.Context, msg *tgbotapi.Message) {
	switch msg.Command() {
	case "start":
		b.handleStart(ctx, msg)
	case "stop":
		b.handleStop(ctx, msg)
	}
}

func (b *Bot) handleStart(ctx context.Context, msg *tgbotapi.Message) {
	token := strings.TrimSpace(msg.CommandArguments())
	if token == "" {
		b.send(msg.Chat.ID, "Зайдите на сайт и нажмите «Привязать Telegram», чтобы получить ссылку для регистрации.")
		return
	}

	tag, err := b.pool.Exec(ctx,
		`UPDATE users
		 SET telegram_chat_id = $1, telegram_username = $2
		 WHERE link_token = $3 AND telegram_chat_id IS NULL`,
		msg.Chat.ID, msg.From.UserName, token,
	)
	if err != nil {
		b.log.Warn().Err(err).Msg("telegram start: db error")
		b.send(msg.Chat.ID, "Внутренняя ошибка. Попробуйте позже.")
		return
	}

	if tag.RowsAffected() == 0 {
		b.send(msg.Chat.ID, "Токен не найден или уже использован. Получите новую ссылку на сайте.")
		return
	}

	b.log.Info().Int64("chat_id", msg.Chat.ID).Str("username", msg.From.UserName).Msg("telegram user linked")
	b.send(msg.Chat.ID, "Привет! Уведомления подключены ✓")
}

func (b *Bot) handleStop(ctx context.Context, msg *tgbotapi.Message) {
	tag, err := b.pool.Exec(ctx,
		`UPDATE users SET telegram_chat_id = NULL WHERE telegram_chat_id = $1`,
		msg.Chat.ID,
	)
	if err != nil {
		b.log.Warn().Err(err).Msg("telegram stop: db error")
		b.send(msg.Chat.ID, "Внутренняя ошибка. Попробуйте позже.")
		return
	}

	if tag.RowsAffected() == 0 {
		b.send(msg.Chat.ID, "Аккаунт не был привязан.")
		return
	}

	b.log.Info().Int64("chat_id", msg.Chat.ID).Msg("telegram user unlinked")
	b.send(msg.Chat.ID, "Уведомления отключены.")
}

func (b *Bot) send(chatID int64, text string) {
	if _, err := b.api.Send(tgbotapi.NewMessage(chatID, text)); err != nil {
		b.log.Warn().Err(err).Int64("chat_id", chatID).Msg("telegram send failed")
	}
}
