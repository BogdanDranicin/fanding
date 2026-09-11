package webpush

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"
)

// Подпись отправителя (VAPID, RFC 8292).
//
// Push-сервису нужно знать, кто шлёт: без подписи он принимал бы уведомления
// для нашей подписки от кого угодно, кто подсмотрел её адрес. Подпись — обычный
// JWT на ключе P-256, где в аудитории стоит адрес самого push-сервиса, а в
// теме — контакт владельца сервиса.

// vapidTTL — на сколько выписывается JWT. Спека разрешает сутки; двенадцать
// часов — запас на перевод часов и разъезд часов сервера с чужими.
const vapidTTL = 12 * time.Hour

type vapidKey struct {
	priv      *ecdsa.PrivateKey
	publicB64 string
	subject   string
}

// newVapidKey читает пару ключей base64url. Открытый ключ нужен нам не для
// подписи, а чтобы отдать его браузеру и положить в заголовок: push-сервис
// сверяет подпись именно с ним.
func newVapidKey(publicKey, privateKey, subject string) (*vapidKey, error) {
	if publicKey == "" || privateKey == "" {
		return nil, errors.New("не заданы ключи VAPID")
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return nil, errors.New("не задан VAPID_SUBJECT: push-сервису нужен контакт владельца")
	}

	pub, err := decodeKey(publicKey)
	if err != nil {
		return nil, fmt.Errorf("открытый ключ VAPID: %w", err)
	}
	if len(pub) != publicKeyLen || pub[0] != 4 {
		return nil, fmt.Errorf("открытый ключ VAPID должен быть несжатой точкой P-256 в %d байт", publicKeyLen)
	}
	raw, err := decodeKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("закрытый ключ VAPID: %w", err)
	}
	if len(raw) != 32 {
		return nil, errors.New("закрытый ключ VAPID должен быть 32 байта")
	}

	curve := elliptic.P256()
	priv := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: curve,
			X:     new(big.Int).SetBytes(pub[1:33]),
			Y:     new(big.Int).SetBytes(pub[33:]),
		},
		D: new(big.Int).SetBytes(raw),
	}
	// Пара обязана сходиться: иначе push-сервис отвергнет подпись, а понять это
	// по его ответу («401 invalid JWT») будет неоткуда.
	x, y := curve.ScalarBaseMult(raw)
	if x.Cmp(priv.X) != 0 || y.Cmp(priv.Y) != 0 {
		return nil, errors.New("ключи VAPID не пара: открытый не выводится из закрытого")
	}

	return &vapidKey{priv: priv, publicB64: b64.EncodeToString(pub), subject: subject}, nil
}

// audience — схема и хост push-сервиса: именно их, без пути, требует RFC 8292.
// Путь адреса — это сама подписка, и класть её в подпись значило бы выписывать
// токен, годный ровно для одного получателя.
func audience(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("адрес подписки без схемы или хоста: %q", endpoint)
	}
	return u.Scheme + "://" + u.Host, nil
}

// authorization собирает заголовок Authorization схемы vapid.
func (k *vapidKey) authorization(endpoint string, now time.Time) (string, error) {
	aud, err := audience(endpoint)
	if err != nil {
		return "", err
	}
	token, err := k.sign(aud, now.Add(vapidTTL))
	if err != nil {
		return "", err
	}
	return "vapid t=" + token + ", k=" + k.publicB64, nil
}

// sign выписывает JWT ES256. Подпись — это R и S подряд по 32 байта, а не
// ASN.1: JWS требует именно такой формат.
func (k *vapidKey) sign(audience string, exp time.Time) (string, error) {
	header := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{
		"aud": audience,
		"exp": exp.Unix(),
		"sub": k.subject,
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + b64.EncodeToString(claims)

	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + b64.EncodeToString(sig), nil
}
