package webpush

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"testing"
)

// Проверка ведётся со стороны браузера: тест расшифровывает запись так, как это
// делает получатель, — своим закрытым ключом подписки и нашим открытым ключом,
// взятым из заголовка записи. Это ровно то, что в работе делает сервис-воркер;
// если наш вывод ключей разойдётся со спекой в порядке ключей, строках info или
// разметке заголовка, расшифровка здесь и сломается.
func openAsBrowser(t *testing.T, uaPriv *ecdh.PrivateKey, auth, record []byte) []byte {
	t.Helper()
	if len(record) < headerLen {
		t.Fatalf("запись короче заголовка: %d байт", len(record))
	}
	salt := record[:saltLen]
	rs := binary.BigEndian.Uint32(record[saltLen : saltLen+4])
	if rs != recordSize {
		t.Errorf("размер записи %d, ждали %d", rs, recordSize)
	}
	idLen := int(record[saltLen+4])
	if idLen != publicKeyLen {
		t.Fatalf("длина keyid %d, ждали %d", idLen, publicKeyLen)
	}
	asPublicRaw := record[saltLen+5 : saltLen+5+idLen]
	body := record[saltLen+5+idLen:]

	asPublic, err := ecdh.P256().NewPublicKey(asPublicRaw)
	if err != nil {
		t.Fatalf("ключ отправителя из заголовка не читается: %v", err)
	}

	shared, err := uaPriv.ECDH(asPublic)
	if err != nil {
		t.Fatalf("ECDH на стороне браузера: %v", err)
	}
	prkKey, err := hkdf.Extract(sha256.New, shared, auth)
	if err != nil {
		t.Fatal(err)
	}
	keyInfo := append([]byte("WebPush: info\x00"), uaPriv.PublicKey().Bytes()...)
	keyInfo = append(keyInfo, asPublicRaw...)
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(keyInfo), sha256.Size)
	if err != nil {
		t.Fatal(err)
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		t.Fatal(err)
	}
	key, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", keyLen)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", nonceLen)
	if err != nil {
		t.Fatal(err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		t.Fatalf("браузер не смог расшифровать запись: %v", err)
	}
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		t.Fatalf("в конце записи должен стоять разделитель 0x02, а стоит % x", plain)
	}
	return plain[:len(plain)-1]
}

func TestEncryptRoundTrip(t *testing.T) {
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}

	msg := []byte(`{"title":"Каждый час","body":"15:00 МСК"}`)
	record, err := encrypt(uaPriv.PublicKey(), auth, msg)
	if err != nil {
		t.Fatalf("шифрование: %v", err)
	}
	if got := openAsBrowser(t, uaPriv, auth, record); !bytes.Equal(got, msg) {
		t.Errorf("расшифровано %q, ждали %q", got, msg)
	}
}

// Чужой секрет подписки не должен подходить: auth — это соль вывода ключа, и
// без него общий секрет ECDH вывел бы всякий, кто знает открытый ключ подписки.
func TestEncryptNeedsAuthSecret(t *testing.T) {
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := bytes.Repeat([]byte{1}, 16)
	other := bytes.Repeat([]byte{2}, 16)

	record, err := encrypt(uaPriv.PublicKey(), auth, []byte("сигнал"))
	if err != nil {
		t.Fatal(err)
	}

	salt := record[:saltLen]
	asPublic, _ := ecdh.P256().NewPublicKey(record[saltLen+5 : saltLen+5+publicKeyLen])
	shared, _ := uaPriv.ECDH(asPublic)
	prkKey, _ := hkdf.Extract(sha256.New, shared, other)
	keyInfo := append([]byte("WebPush: info\x00"), uaPriv.PublicKey().Bytes()...)
	keyInfo = append(keyInfo, asPublic.Bytes()...)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, string(keyInfo), sha256.Size)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	key, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", keyLen)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", nonceLen)

	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	if _, err := gcm.Open(nil, nonce, record[headerLen:], nil); err == nil {
		t.Fatal("запись расшифровалась чужим секретом подписки")
	}
}

// Заголовок записи разбирается получателем по фиксированной разметке RFC 8188:
// соль, размер записи, длина ключа, ключ. Сдвинувшись в ней на байт, мы сломали
// бы всех получателей сразу и молча.
func TestEncryptHeaderLayout(t *testing.T) {
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	as, _ := ecdh.P256().GenerateKey(rand.Reader)
	salt := bytes.Repeat([]byte{7}, saltLen)

	record, err := encryptWith(uaPriv.PublicKey(), bytes.Repeat([]byte{3}, 16), []byte("ok"), as, salt)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(record[:saltLen], salt) {
		t.Errorf("соль записи % x, ждали % x", record[:saltLen], salt)
	}
	if got := binary.BigEndian.Uint32(record[saltLen : saltLen+4]); got != recordSize {
		t.Errorf("размер записи %d, ждали %d", got, recordSize)
	}
	if !bytes.Equal(record[saltLen+5:saltLen+5+publicKeyLen], as.PublicKey().Bytes()) {
		t.Error("в keyid должен лежать наш одноразовый открытый ключ")
	}
	// Длина записи задана целиком: заголовок, сообщение, разделитель и тег GCM.
	if want := headerLen + len("ok") + overhead; len(record) != want {
		t.Errorf("длина записи %d, ждали %d", len(record), want)
	}
}

// Сообщение длиннее записи — это ошибка отправителя, а не молчаливая порча:
// push-сервис всё равно не возьмёт больше четырёх килобайт.
func TestEncryptRejectsOversized(t *testing.T) {
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	if _, err := encrypt(uaPriv.PublicKey(), make([]byte, 16), make([]byte, recordSize)); err == nil {
		t.Fatal("слишком длинное сообщение принято")
	}
}
