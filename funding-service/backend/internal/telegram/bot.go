package telegram

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

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

// New creates a Bot, optionally routing Telegram traffic through one of proxyURLs.
// Returns an error if the token is empty/invalid or no proxy could authorise.
func New(token string, proxyURLs []string, pool *pgxpool.Pool, log zerolog.Logger) (*Bot, error) {
	api, err := newAPI(token, proxyURLs, log)
	if err != nil {
		return nil, err
	}
	log.Info().Str("username", api.Self.UserName).Msg("telegram bot authorised")
	return &Bot{api: api, pool: pool, log: log}, nil
}

// newAPI authorises the bot, directly or through the first working proxy.
// api.telegram.org is unreachable from some networks (e.g. RU), so a proxy that
// can reach it is required there; proxies are tried in order until one works.
func newAPI(token string, proxyURLs []string, log zerolog.Logger) (*tgbotapi.BotAPI, error) {
	if len(nonEmpty(proxyURLs)) == 0 {
		return tgbotapi.NewBotAPI(token)
	}
	var lastErr error
	for _, raw := range proxyURLs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		client, err := proxyClient(raw)
		if err != nil {
			lastErr = err
			log.Warn().Err(err).Msg("telegram: skipping malformed proxy url")
			continue
		}
		api, err := tgbotapi.NewBotAPIWithClient(token, tgbotapi.APIEndpoint, client)
		if err != nil {
			lastErr = err
			log.Warn().Err(err).Str("proxy", proxyHost(raw)).Msg("telegram: proxy failed, trying next")
			continue
		}
		log.Info().Str("proxy", proxyHost(raw)).Msg("telegram: connected via proxy")
		return api, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no usable proxy in TELEGRAM_PROXY_URL")
	}
	return nil, fmt.Errorf("all telegram proxies failed: %w", lastErr)
}

// proxyClient builds an HTTP client that tunnels through the given proxy.
// A bare "user:pass@host:port" (no scheme) is treated as http://. No overall
// client Timeout is set: Telegram long-polling holds a request open ~30 s.
func proxyClient(raw string) (*http.Client, error) {
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		Proxy:               http.ProxyURL(u),
		DialContext:         (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	return &http.Client{Transport: tr}, nil
}

// proxyHost returns the host:port of a proxy URL, stripping credentials so they
// never reach the logs.
func proxyHost(raw string) string {
	if i := strings.LastIndex(raw, "@"); i >= 0 {
		return raw[i+1:]
	}
	return raw
}

func nonEmpty(ss []string) []string {
	out := ss[:0:0]
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
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
