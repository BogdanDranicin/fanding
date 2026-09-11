package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/config"
	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/storage"
	"github.com/funding-service/backend/internal/webpush"
)

// Диспетчер push-будильников.
//
// Страница кладёт в очередь ближайшие отметки своего расписания, диспетчер
// раз в такт забирает всё, чему пора, и будит браузеры. Это единственный путь
// сигнала до замороженной вкладки: в ней не выполняются ни таймеры, ни звук,
// поставленный в очередь WebAudio заранее, а сервис-воркер push будит всегда.

const (
	// pushTick — как часто диспетчер заглядывает в очередь. Отметки задаются с
	// точностью до секунды, и полусекундный такт даёт запас на округление.
	pushTick = 500 * time.Millisecond
	// pushLead — насколько раньше отметки уходит уведомление. Дорога до
	// устройства через чужой push-сервис занимает доли секунды, и без форы
	// сигнал систематически опаздывал бы на эту дорогу.
	pushLead = 300 * time.Millisecond
	// pushStale — насколько просроченную отметку ещё есть смысл доставлять.
	// Всё, что старше, выбрасывается: сервис мог лежать полчаса, и вываливать
	// пользователю пачку прошлогодних будильников — худшее, что можно сделать.
	pushStale = 2 * time.Minute
	// pushTTL — сколько push-сервис хранит сообщение, если устройство сейчас
	// недоступно. Дольше держать незачем: сигнал по времени к тому моменту уже
	// ничего не значит.
	pushTTL = time.Minute
	// pushBatch/pushWorkers — сколько отметок берём за такт и сколько отправок
	// идёт разом. Последовательная отправка упиралась бы в чужую сеть: сто
	// уведомлений по 200 мс — это двадцать секунд на такт в полсекунды.
	pushBatch   = 100
	pushWorkers = 8
)

// newPushSender собирает отправителя push, если ключи VAPID заданы. Без них
// сервис работает как раньше — страница просто не предложит уведомления.
func newPushSender(cfg *config.Config, log zerolog.Logger) *webpush.Sender {
	if cfg.VAPIDPublicKey == "" || cfg.VAPIDPrivateKey == "" {
		log.Info().Msg("push: ключи VAPID не заданы, уведомления выключены")
		return nil
	}
	sender, err := webpush.NewSender(
		cfg.VAPIDPublicKey, cfg.VAPIDPrivateKey, cfg.VAPIDSubject, pushHTTPClient(cfg.PushProxyURL),
	)
	if err != nil {
		// Не Fatal: молчащие уведомления — это неприятно, а упавший сервис —
		// это нет фандинга вовсе.
		log.Error().Err(err).Msg("push: ключи VAPID не приняты, уведомления выключены")
		return nil
	}
	log.Info().Bool("proxy", cfg.PushProxyURL != "").Msg("push: уведомления включены")
	return sender
}

// pushHTTPClient — клиент до push-сервиса, при необходимости через прокси.
// Прокси здесь по той же причине, что и у телеграм-бота: с некоторых сетей
// чужие сервисы просто недоступны.
func pushHTTPClient(proxyURL string) *http.Client {
	tr := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     5 * time.Minute,
	}
	if proxyURL != "" {
		raw := proxyURL
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		if u, err := url.Parse(raw); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Transport: tr, Timeout: 15 * time.Second}
}

// pushPublicKey — ключ для страницы; пустая строка, если push не настроен.
func pushPublicKey(sender *webpush.Sender) string {
	if sender == nil {
		return ""
	}
	return sender.PublicKey()
}

// runPushDispatcher ведёт очередь будильников до отмены контекста.
func runPushDispatcher(ctx context.Context, store *storage.Store, sender *webpush.Sender, log zerolog.Logger) {
	if sender == nil || store == nil {
		return
	}
	log = log.With().Str("component", "push").Logger()
	ticker := time.NewTicker(pushTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			dispatchDuePush(ctx, store, sender, log)
		}
	}
}

func dispatchDuePush(ctx context.Context, store *storage.Store, sender *webpush.Sender, log zerolog.Logger) {
	due, err := store.DuePushWakeups(ctx, time.Now().Add(pushLead), pushStale, pushBatch)
	if err != nil {
		if ctx.Err() == nil {
			log.Warn().Err(err).Msg("push: очередь не прочитана")
		}
		return
	}
	if len(due) == 0 {
		return
	}

	jobs := make(chan storage.DuePushWakeup)
	var wg sync.WaitGroup
	for range min(pushWorkers, len(due)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range jobs {
				sendWakeup(ctx, store, sender, d, log)
			}
		}()
	}
	for _, d := range due {
		jobs <- d
	}
	close(jobs)
	wg.Wait()
}

func sendWakeup(
	ctx context.Context, store *storage.Store, sender *webpush.Sender,
	d storage.DuePushWakeup, log zerolog.Logger,
) {
	payload, err := json.Marshal(map[string]string{
		"title": d.Wakeup.Title,
		"body":  d.Wakeup.Body,
		"tag":   d.Wakeup.Tag,
	})
	if err != nil {
		return
	}

	err = sender.Send(ctx, webpush.Subscription{
		Endpoint: d.Subscription.Endpoint,
		P256dh:   d.Subscription.P256dh,
		Auth:     d.Subscription.Auth,
	}, payload, pushTTL)

	switch {
	case err == nil:
		log.Debug().Str("title", d.Wakeup.Title).
			Dur("late", time.Since(d.Wakeup.FireAt)).Msg("push: уведомление отправлено")
	case errors.Is(err, webpush.ErrGone):
		// Браузер отозвал подписку: пользователь запретил уведомления или снёс
		// сайт. Долбиться в такой адрес бессмысленно — строку убираем.
		if err := store.DeletePushSubscription(context.WithoutCancel(ctx), d.Subscription.Endpoint); err != nil {
			log.Warn().Err(err).Msg("push: мёртвая подписка не удалена")
		} else {
			log.Info().Msg("push: подписка отозвана браузером, удалена")
		}
	case ctx.Err() != nil:
		// Сервис останавливается — это не отказ доставки.
	default:
		log.Warn().Err(err).Str("title", d.Wakeup.Title).Msg("push: уведомление не доставлено")
	}
}

// Уведомление о публикации курса ЦБ.
//
// Это второй звук сервиса (первый — сигналы по времени), и он ровно так же
// пропадал в заморожённой вкладке: WebSocket в ней не работает, снапшот не
// приходит, играть нечего. Поэтому публикация тоже уходит push-ом — тем же
// каналом и тем же ключом, только мимо очереди: ждать её нечего, она уже
// случилась.

const (
	// pushFundingWait — сколько ждём, пока движок пересчитает точный фандинг.
	// Сигнал о публикации летит параллельно тикам с новыми курсами, и снапшот в
	// момент сигнала ещё вчерашний (та же причина, что у телеграм-рассылки).
	pushFundingWait = 10 * time.Second
	pushFundingStep = 20 * time.Millisecond
)

// runPushPublications рассылает уведомление о каждой публикации курса ЦБ.
func runPushPublications(
	ctx context.Context, store *storage.Store, sender *webpush.Sender,
	snapshotFn func() funding.FundingSnapshot, pubCh <-chan time.Time, log zerolog.Logger,
) {
	if sender == nil || store == nil {
		return
	}
	log = log.With().Str("component", "push").Logger()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-pubCh:
			if !ok {
				return
			}
			snap := awaitPushFunding(ctx, snapshotFn)
			body := fundingLine(snap)
			if body == "" {
				// Публикация без пересчитанного фандинга: показывать пустое
				// уведомление хуже, чем не показывать никакого.
				log.Warn().Msg("push: публикация без фандинга, уведомление не отправлено")
				continue
			}
			broadcastPush(ctx, store, sender, "Фандинг зафиксирован", body, "cb-funding", log)
		}
	}
}

// awaitPushFunding ждёт снапшот, в котором точный фандинг уже посчитан.
func awaitPushFunding(ctx context.Context, snapshotFn func() funding.FundingSnapshot) funding.FundingSnapshot {
	deadline := time.Now().Add(pushFundingWait)
	for {
		snap := snapshotFn()
		if snap.USDRUBF.CBFunding != nil || time.Now().After(deadline) {
			return snap
		}
		select {
		case <-ctx.Done():
			return snap
		case <-time.After(pushFundingStep):
		}
	}
}

// fundingLine — строка уведомления: ставки в том же виде, в каком их публикует
// биржа (SWAPRATE, пять знаков). Пустая строка означает, что показывать нечего.
func fundingLine(snap funding.FundingSnapshot) string {
	parts := make([]string, 0, 2)
	if v := snap.USDRUBF.CBFunding; v != nil {
		parts = append(parts, fmt.Sprintf("USD %+.5f", *v))
	}
	if v := snap.EURRUBF.CBFunding; v != nil {
		parts = append(parts, fmt.Sprintf("EUR %+.5f", *v))
	}
	return strings.Join(parts, " · ")
}

// broadcastPush шлёт одно уведомление всем браузерам, которые его ждут.
func broadcastPush(
	ctx context.Context, store *storage.Store, sender *webpush.Sender,
	title, body, tag string, log zerolog.Logger,
) {
	subs, err := store.FundingPushSubscriptions(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("push: список подписок не прочитан")
		return
	}
	if len(subs) == 0 {
		return
	}
	payload, err := json.Marshal(map[string]string{"title": title, "body": body, "tag": tag})
	if err != nil {
		return
	}

	jobs := make(chan storage.PushSubscription)
	var wg sync.WaitGroup
	for range min(pushWorkers, len(subs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sub := range jobs {
				err := sender.Send(ctx, webpush.Subscription{
					Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: sub.Auth,
				}, payload, pushTTL)
				switch {
				case err == nil:
				case errors.Is(err, webpush.ErrGone):
					_ = store.DeletePushSubscription(context.WithoutCancel(ctx), sub.Endpoint)
				case ctx.Err() != nil:
				default:
					log.Warn().Err(err).Msg("push: уведомление о публикации не доставлено")
				}
			}
		}()
	}
	for _, sub := range subs {
		jobs <- sub
	}
	close(jobs)
	wg.Wait()
	log.Info().Int("subscriptions", len(subs)).Str("body", body).Msg("push: публикация разослана")
}
