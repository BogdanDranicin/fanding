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

	"github.com/funding-service/backend/internal/storage"
)

// Bot wraps a Telegram bot and handles user registration via link tokens.
type Bot struct {
	api    *tgbotapi.BotAPI
	pool   *pgxpool.Pool
	store  *storage.Store
	admins AdminList
	log    zerolog.Logger
}

// New creates a Bot, optionally routing Telegram traffic through one of proxyURLs.
// Returns an error if the token is empty/invalid or no proxy could authorise.
func New(token string, proxyURLs []string, pool *pgxpool.Pool, store *storage.Store, admins AdminList, log zerolog.Logger) (*Bot, error) {
	api, err := newAPI(token, proxyURLs, log)
	if err != nil {
		return nil, err
	}
	log.Info().Str("username", api.Self.UserName).Int("admins", len(admins)).Msg("telegram bot authorised")
	return &Bot{api: api, pool: pool, store: store, admins: admins, log: log}, nil
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
//
// Пул соединений расширен и держится долго намеренно. Долгий поллинг GetUpdates
// занимает своё соединение постоянно (оно НЕ idle, переиспользовать его нельзя),
// поэтому рассылка всегда идёт по отдельному; при дефолтных настройках оно
// протухало между публикациями, и первое сообщение дня платило полный дозвон
// через прокси плюс TLS-handshake — сотни миллисекунд. keepWarm держит это
// соединение живым, а MaxIdleConnsPerHost позволяет параллельным отправкам
// не дозваниваться заново.
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
		DialContext:         (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 12,
		IdleConnTimeout:     5 * time.Minute,
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

// keepWarmInterval — как часто пинговать api.telegram.org, чтобы в пуле всегда
// лежало готовое соединение. Должно быть заметно меньше IdleConnTimeout.
const keepWarmInterval = 60 * time.Second

// keepWarm раз в минуту дёргает getMe. Смысл не в ответе, а в соединении:
// публикация ЦБ случается раз в сутки, и без пинга рассылка каждый раз начиналась
// с холодного дозвона через прокси и TLS-handshake — это и была основная часть
// отставания Telegram от сайта. Один дешёвый запрос в минуту (лимитов Telegram
// не касается) держит соединение горячим, так что Send уходит сразу.
func (b *Bot) keepWarm(ctx context.Context) {
	t := time.NewTicker(keepWarmInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := b.api.GetMe(); err != nil {
				b.log.Debug().Err(err).Msg("telegram: keep-warm ping failed")
			}
		}
	}
}

// Run starts long-polling and blocks until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	go b.keepWarm(ctx)

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

// handleStart привязывает чат к сессии браузера, из которой пришла ссылка.
//
// Повторный /start из уже подписанного чата — это НЕ ошибка: человек открыл сайт
// в другом браузере (или почистил его хранилище), там завелась новая сессия, и он
// жмёт «Привязать Telegram» ещё раз. Раньше бот отвечал «вы уже подписаны», а тот
// браузер так и оставался анонимным и вечно показывал виджет привязки. Теперь
// сессия переезжает на существующий аккаунт (storage.LinkedMerged).
func (b *Bot) handleStart(ctx context.Context, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	isAdmin := b.admins.Has(chatID, msg.From.UserName)

	token := strings.TrimSpace(msg.CommandArguments())
	if token == "" {
		// Голый /start: ссылки нет — привязывать нечего, но роль по списку
		// админов освежим, если чат уже подписан.
		if err := b.store.SetAdminByChat(ctx, chatID, isAdmin); err != nil {
			b.log.Warn().Err(err).Msg("telegram start: admin refresh failed")
		}
		b.send(chatID, "Зайдите на сайт и нажмите «Привязать Telegram», чтобы получить ссылку для регистрации.")
		return
	}

	res, err := b.store.LinkTelegramChat(ctx, token, chatID, msg.From.UserName, isAdmin)
	if errors.Is(err, storage.ErrLinkTokenNotFound) {
		b.send(chatID, "Ссылка устарела. Откройте сайт и нажмите «Привязать Telegram» ещё раз.")
		return
	}
	if err != nil {
		b.log.Warn().Err(err).Msg("telegram start: db error")
		b.send(chatID, "Внутренняя ошибка. Попробуйте позже.")
		return
	}

	b.log.Info().
		Int64("chat_id", chatID).
		Str("username", msg.From.UserName).
		Bool("admin", isAdmin).
		Int("result", int(res)).
		Msg("telegram user linked")

	switch res {
	case storage.LinkedMerged:
		b.send(chatID, "Этот браузер подключён к вашей подписке ✓")
	case storage.LinkedSameUser:
		b.send(chatID, "Вы уже подписаны на уведомления ✓")
	default:
		b.send(chatID, "Привет! Уведомления подключены ✓")
	}
}

func (b *Bot) handleStop(ctx context.Context, msg *tgbotapi.Message) {
	was, err := b.store.UnlinkTelegramChat(ctx, msg.Chat.ID)
	if err != nil {
		b.log.Warn().Err(err).Msg("telegram stop: db error")
		b.send(msg.Chat.ID, "Внутренняя ошибка. Попробуйте позже.")
		return
	}

	if !was {
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
