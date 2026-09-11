package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/funding-service/backend/internal/storage"
)

// Подписка браузера на push и его расписание будильников.
//
// Расписание сигналов по времени живёт в браузере — сервер его не ведёт и не
// хочет. Сюда приезжает только выжимка: ближайшие отметки, к которым браузер
// надо разбудить. Пока страница открыта, она перекладывает список заново после
// каждой правки; закрытая или замороженная — доигрывает то, что успела положить.

const (
	// maxWakeups — сколько отметок берём за раз. Горизонт у страницы полсуток,
	// самый частый сигнал — раз в минуту; упираться в потолок будет только он,
	// и упереться ему полезно: очередь на сутки вперёд никому не нужна.
	maxWakeups = 64
	// wakeupHorizon — насколько вперёд принимаем отметки. Всё дальше — либо
	// сбитые часы браузера, либо попытка забить очередь.
	wakeupHorizon = 24 * time.Hour
	// wakeupSlack — насколько отметка может отстать от часов сервера и всё ещё
	// иметь смысл: часы браузера и сервера всегда немного расходятся.
	wakeupSlack = time.Minute

	maxEndpointLen = 2048
	maxTitleLen    = 80
	maxBodyLen     = 160
	maxTagLen      = 80
)

type pushKeysJSON struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

type pushSubscribeRequest struct {
	Endpoint string       `json:"endpoint"`
	Keys     pushKeysJSON `json:"keys"`
	// Funding — слать ли уведомление о публикации курса ЦБ: это отдельная
	// галочка звука в настройках, и браузер сообщает её состояние вместе с
	// подпиской.
	Funding bool `json:"funding"`
}

type pushWakeupJSON struct {
	// At — момент срабатывания в миллисекундах эпохи: страница считает
	// расписание в этой же шкале, и перевод в строку по дороге только добавил
	// бы поводов разойтись на часовом поясе.
	At    int64  `json:"at"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Tag   string `json:"tag"`
}

type pushWakeupsRequest struct {
	Endpoint string           `json:"endpoint"`
	Wakeups  []pushWakeupJSON `json:"wakeups"`
}

// handlePushKey отдаёт открытый ключ VAPID — его браузер кладёт в подписку.
// Пустой ключ означает, что push на этом сервисе не настроен: страница тогда
// не предлагает включить уведомления вовсе, вместо того чтобы обещать несбыточное.
func handlePushKey(publicKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":    publicKey != "",
			"public_key": publicKey,
		})
	}
}

// validEndpoint — адрес подписки обязан быть чужим https-адресом. Проверка не
// косметическая: сервис ходит по этому адресу сам, и без неё в него можно было
// бы подсунуть внутренний адрес сети.
func validEndpoint(raw string) bool {
	if raw == "" || len(raw) > maxEndpointLen {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

// handlePushSubscribe запоминает подписку браузера за его сессией.
func handlePushSubscribe(store *storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pushSubscribeRequest
		if !decodeBody(w, r, &req) {
			return
		}
		if !validEndpoint(req.Endpoint) || req.Keys.P256dh == "" || req.Keys.Auth == "" {
			http.Error(w, "bad subscription", http.StatusBadRequest)
			return
		}
		session := bearerToken(r)
		if _, err := store.SavePushSubscription(r.Context(), session, storage.PushSubscription{
			Endpoint: req.Endpoint,
			P256dh:   req.Keys.P256dh,
			Auth:     req.Keys.Auth,
			Funding:  req.Funding,
		}); err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handlePushUnsubscribe убирает подписку и её очередь.
func handlePushUnsubscribe(store *storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pushSubscribeRequest
		if !decodeBody(w, r, &req) {
			return
		}
		// Удаляем только свою подписку: адрес — это ключ от уведомлений браузера,
		// и чужой по нему отписать нельзя.
		sub, err := store.PushSubscriptionByEndpoint(r.Context(), bearerToken(r), req.Endpoint)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		if err := store.DeletePushSubscription(r.Context(), sub.Endpoint); err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// pushTestDelay — через сколько приходит проверочное уведомление. Пяти секунд
// хватает, чтобы успеть свернуть окно и увидеть ровно то, ради чего всё это
// затевалось: сигнал приходит в невидимую вкладку.
const pushTestDelay = 5 * time.Second

// handlePushTest ставит одно проверочное уведомление, не трогая расписание.
func handlePushTest(store *storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pushSubscribeRequest
		if !decodeBody(w, r, &req) {
			return
		}
		sub, err := store.PushSubscriptionByEndpoint(r.Context(), bearerToken(r), req.Endpoint)
		if err != nil {
			http.Error(w, "subscription not found", http.StatusNotFound)
			return
		}
		if err := store.AddPushWakeup(r.Context(), sub.ID, storage.PushWakeup{
			FireAt: time.Now().Add(pushTestDelay),
			Title:  "Проверка сигнала",
			Body:   "Уведомление дошло — дойдёт и будильник",
			Tag:    "push-test",
		}); err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"in_seconds": int(pushTestDelay.Seconds())})
	}
}

// clip укорачивает строку по рунам, а не по байтам: русский текст в байтах
// вдвое длиннее, и обрезанный по ним он развалился бы посреди буквы.
func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// wakeupsFrom приводит присланное расписание к тому, что имеет смысл хранить:
// отбрасывает отметки из прошлого и из слишком далёкого будущего, режет длинные
// строки и не берёт больше maxWakeups.
func wakeupsFrom(list []pushWakeupJSON, now time.Time) []storage.PushWakeup {
	out := make([]storage.PushWakeup, 0, len(list))
	for _, w := range list {
		if len(out) >= maxWakeups {
			break
		}
		at := time.UnixMilli(w.At)
		if at.Before(now.Add(-wakeupSlack)) || at.After(now.Add(wakeupHorizon)) {
			continue
		}
		title := clip(w.Title, maxTitleLen)
		if title == "" {
			continue
		}
		out = append(out, storage.PushWakeup{
			FireAt: at,
			Title:  title,
			Body:   clip(w.Body, maxBodyLen),
			Tag:    clip(w.Tag, maxTagLen),
		})
	}
	return out
}

// handlePushWakeups заменяет очередь браузера присланным расписанием.
func handlePushWakeups(store *storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pushWakeupsRequest
		if !decodeBody(w, r, &req) {
			return
		}
		sub, err := store.PushSubscriptionByEndpoint(r.Context(), bearerToken(r), req.Endpoint)
		if err != nil {
			// Подписки нет — браузер отстал от сервера (база переехала, строку
			// вычистили после 410). Пусть подпишется заново.
			http.Error(w, "subscription not found", http.StatusNotFound)
			return
		}

		wakeups := wakeupsFrom(req.Wakeups, time.Now())
		if err := store.ReplacePushWakeups(r.Context(), sub.ID, wakeups); err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"accepted": len(wakeups)})
	}
}
