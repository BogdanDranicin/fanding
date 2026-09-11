package webpush

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testKeys — пара VAPID в том виде, в каком её печатает cmd/vapidkeys и кладут
// в переменные окружения.
func testKeys(t *testing.T) (pub, priv string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 32)
	key.D.FillBytes(raw)
	point := append([]byte{4}, append(key.X.FillBytes(make([]byte, 32)), key.Y.FillBytes(make([]byte, 32))...)...)
	return b64.EncodeToString(point), b64.EncodeToString(raw)
}

// Заголовок разбирается push-сервисом: схема vapid, JWT в t= и наш открытый
// ключ в k=. Подпись обязана сходиться с этим самым ключом — иначе сервис
// отвечает 401, и по его ответу причину не найти.
func TestVapidAuthorization(t *testing.T) {
	pub, priv := testKeys(t)
	key, err := newVapidKey(pub, priv, "mailto:admin@example.com")
	if err != nil {
		t.Fatal(err)
	}

	header, err := key.authorization("https://fcm.googleapis.com/fcm/send/abc123", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rest, ok := strings.CutPrefix(header, "vapid t=")
	if !ok {
		t.Fatalf("заголовок начинается не со схемы vapid: %q", header)
	}
	token, keyPart, ok := strings.Cut(rest, ", k=")
	if !ok {
		t.Fatalf("в заголовке нет ключа отправителя: %q", header)
	}
	if keyPart != pub {
		t.Errorf("в k= лежит %q, ждали открытый ключ %q", keyPart, pub)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT из %d частей: %q", len(parts), token)
	}

	var claims struct {
		Aud string `json:"aud"`
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	body, err := b64.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &claims); err != nil {
		t.Fatal(err)
	}
	// Аудитория — только схема и хост: путь адреса это сама подписка, и токен,
	// выписанный на неё, не годился бы ни для одной другой.
	if claims.Aud != "https://fcm.googleapis.com" {
		t.Errorf("aud = %q", claims.Aud)
	}
	if claims.Sub != "mailto:admin@example.com" {
		t.Errorf("sub = %q", claims.Sub)
	}
	if left := time.Until(time.Unix(claims.Exp, 0)); left <= 0 || left > 24*time.Hour {
		t.Errorf("срок токена %v — вне разрешённых суток", left)
	}

	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 64 {
		t.Fatalf("подпись %d байт, JWS требует 64 (R и S подряд)", len(sig))
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.priv.PublicKey, sum[:], r, s) {
		t.Error("подпись не сходится с открытым ключом из заголовка")
	}
}

// Неполная или битая настройка должна отказываться на старте сервиса, а не
// оборачиваться молчащими уведомлениями в проде.
func TestVapidKeyRejectsBadConfig(t *testing.T) {
	pub, priv := testKeys(t)
	otherPub, _ := testKeys(t)

	cases := map[string]struct{ pub, priv, subject string }{
		"без ключей":    {"", "", "mailto:a@b.c"},
		"без темы":      {pub, priv, "  "},
		"мусор в ключе": {pub, "не base64!!", "mailto:a@b.c"},
		"короткий ключ": {pub, b64.EncodeToString([]byte("коротко")), "mailto:a@b.c"},
		"ключи не пара": {otherPub, priv, "mailto:a@b.c"},
	}
	for name, c := range cases {
		if _, err := newVapidKey(c.pub, c.priv, c.subject); err == nil {
			t.Errorf("%s: настройка принята, хотя работать она не будет", name)
		}
	}
}

// Отправка целиком: подписка → шифрование → запрос. Проверяем то, по чему
// push-сервис принимает решение, — заголовки и тело.
func TestSenderRequest(t *testing.T) {
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}

	var got *http.Request
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	pub, priv := testKeys(t)
	sender, err := NewSender(pub, priv, "mailto:admin@example.com", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	sub := Subscription{
		Endpoint: srv.URL + "/push/abc",
		P256dh:   b64.EncodeToString(uaPriv.PublicKey().Bytes()),
		Auth:     b64.EncodeToString(auth),
	}
	msg := []byte(`{"title":"Сигнал"}`)
	if err := sender.Send(context.Background(), sub, msg, 30*time.Second); err != nil {
		t.Fatalf("отправка: %v", err)
	}

	if got.Header.Get("Content-Encoding") != "aes128gcm" {
		t.Errorf("Content-Encoding = %q", got.Header.Get("Content-Encoding"))
	}
	if got.Header.Get("TTL") != "30" {
		t.Errorf("TTL = %q", got.Header.Get("TTL"))
	}
	if !strings.HasPrefix(got.Header.Get("Authorization"), "vapid t=") {
		t.Errorf("Authorization = %q", got.Header.Get("Authorization"))
	}
	if plain := openAsBrowser(t, uaPriv, auth, body); string(plain) != string(msg) {
		t.Errorf("до браузера доехало %q, ждали %q", plain, msg)
	}
}

// 410 Gone от push-сервиса означает, что подписки больше нет: строку надо
// удалить, а не долбиться в неё до скончания века.
func TestSenderGone(t *testing.T) {
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()

	pub, priv := testKeys(t)
	sender, _ := NewSender(pub, priv, "mailto:admin@example.com", srv.Client())
	err := sender.Send(context.Background(), Subscription{
		Endpoint: srv.URL + "/push/abc",
		P256dh:   b64.EncodeToString(uaPriv.PublicKey().Bytes()),
		Auth:     b64.EncodeToString(make([]byte, 16)),
	}, []byte("{}"), time.Minute)
	if !errors.Is(err, ErrGone) {
		t.Fatalf("ошибка %v, ждали ErrGone", err)
	}
}
