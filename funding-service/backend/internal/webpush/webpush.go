// Package webpush отправляет уведомления браузеру через его push-сервис
// (RFC 8030) — тем самым каналом, которым пользуются все сайты с уведомлениями.
//
// Зачем он здесь. Сигналы по времени и звук о публикации фандинга жили целиком
// в браузере: страница считала расписание и играла ноту. Пока вкладка открыта и
// на неё смотрят, этого хватает; но браузер ЗАМОРАЖИВАЕТ вкладку, которую не
// открывали минут пять, — в замороженной странице не выполняется ни один таймер
// и не идёт звук, поставленный в очередь заранее. Удерживать вкладку живой
// пробовали и локом, и неслышимым звуком: лок браузер перестал считать поводом
// не морозить, а неслышимый звук — это значок динамика на вкладке и стыд.
//
// Push ничего этого не требует: сервис-воркер будит браузер сам, даже если
// вкладка заморожена, окно свёрнуто или браузер закрыт совсем.
//
// Реализация без внешних зависимостей: шифрование содержимого — RFC 8291
// (aes128gcm поверх RFC 8188), подпись отправителя — VAPID (RFC 8292). Всё, что
// для этого нужно, есть в стандартной библиотеке.
package webpush

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// ErrGone — подписка мертва: браузер её отозвал (пользователь запретил
// уведомления, снёс сайт из браузера, сменил устройство). Такую строку из базы
// надо удалить, а не повторять отправку.
var ErrGone = errors.New("push subscription gone")

// Subscription — то, что браузер отдаёт в PushSubscription.toJSON().
type Subscription struct {
	Endpoint string `json:"endpoint"`
	// P256dh — открытый ключ браузера, base64url, 65 байт в несжатом виде.
	P256dh string `json:"p256dh"`
	// Auth — общий секрет подписки, base64url, 16 байт.
	Auth string `json:"auth"`
}

// b64 — base64url без выравнивания: в этом виде ключи ходят и в вебе, и в VAPID.
var b64 = base64.RawURLEncoding

// decodeKey принимает ключ и с выравниванием, и без: браузеры отдают его
// по-разному, а отвалиться на чужом хвосте из «=» было бы глупо.
func decodeKey(s string) ([]byte, error) {
	if raw, err := b64.DecodeString(s); err == nil {
		return raw, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// Sender шлёт уведомления от имени сервиса. Один на весь процесс: в нём живёт
// ключ VAPID и пул соединений к push-сервисам.
type Sender struct {
	vapid  *vapidKey
	client *http.Client
}

// NewSender собирает отправителя. Ключи — base64url, как их печатает
// cmd/vapidkeys; subject — mailto: или адрес сайта, по нему push-сервис ищет
// владельца, если с отправкой что-то не так.
//
// client может быть nil — тогда обычный http.Client с таймауном. Отдельный
// клиент нужен там, где push-сервис доступен только через прокси.
func NewSender(publicKey, privateKey, subject string, client *http.Client) (*Sender, error) {
	key, err := newVapidKey(publicKey, privateKey, subject)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Sender{vapid: key, client: client}, nil
}

// PublicKey — открытый ключ VAPID в том виде, в каком его ждёт браузер в
// applicationServerKey.
func (s *Sender) PublicKey() string { return s.vapid.publicB64 }

// Send доставляет payload одной подписке. ttl — сколько push-сервис хранит
// сообщение, если устройство сейчас недоступно.
func (s *Sender) Send(ctx context.Context, sub Subscription, payload []byte, ttl time.Duration) error {
	uaPublic, err := decodeKey(sub.P256dh)
	if err != nil {
		return fmt.Errorf("ключ подписки: %w", err)
	}
	auth, err := decodeKey(sub.Auth)
	if err != nil {
		return fmt.Errorf("секрет подписки: %w", err)
	}
	ua, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return fmt.Errorf("ключ подписки не на кривой P-256: %w", err)
	}

	body, err := encrypt(ua, auth, payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	auths, err := s.vapid.authorization(sub.Endpoint, time.Now())
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auths)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(ttl.Seconds())))
	// Urgency: high — сигнал по времени бесполезен, если доехал позже своей
	// отметки; push-сервису это разрешает будить устройство сразу.
	req.Header.Set("Urgency", "high")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Тело ответа читаем целиком и коротко: без этого соединение не вернётся
	// в пул, а в ошибке push-сервиса лежит единственное объяснение отказа.
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrGone
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	default:
		return fmt.Errorf("push-сервис ответил %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
}
