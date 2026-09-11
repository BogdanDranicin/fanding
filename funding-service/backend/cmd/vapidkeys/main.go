// Команда vapidkeys печатает пару ключей VAPID для push-уведомлений.
//
// Ключи заводятся один раз на сервис и живут в переменных окружения. Менять их
// нельзя без нужды: открытый ключ вшит в каждую выданную браузером подписку, и
// с новой парой все подписки разом перестают приниматься — пользователям
// придётся включать уведомления заново.
//
//	go run ./cmd/vapidkeys
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

func main() {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, "не удалось сгенерировать ключ:", err)
		os.Exit(1)
	}

	b64 := base64.RawURLEncoding
	private := make([]byte, 32)
	key.D.FillBytes(private)
	public := make([]byte, 0, 65)
	public = append(public, 4)
	public = append(public, key.X.FillBytes(make([]byte, 32))...)
	public = append(public, key.Y.FillBytes(make([]byte, 32))...)

	fmt.Printf("VAPID_PUBLIC_KEY=%s\n", b64.EncodeToString(public))
	fmt.Printf("VAPID_PRIVATE_KEY=%s\n", b64.EncodeToString(private))
	fmt.Println("VAPID_SUBJECT=mailto:you@example.com")
}
