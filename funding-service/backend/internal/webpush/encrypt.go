package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// Шифрование содержимого push (RFC 8291 поверх RFC 8188).
//
// Push-сервис — чужой сервер: он видит запрос, но не должен видеть, что внутри.
// Поэтому содержимое шифруется ключом, который знают только сервис и браузер:
// общий секрет ECDH между нашим одноразовым ключом и ключом подписки, посоленный
// секретом подписки (auth). Браузер получает наш открытый ключ прямо в заголовке
// записи и выводит тот же ключ у себя.
//
// Всё, что для этого нужно, есть в стандартной библиотеке: crypto/ecdh даёт
// обмен, crypto/hkdf — вывод ключей, crypto/aes — саму запись.

const (
	// recordSize — размер записи RFC 8188. Наши сообщения — несколько сотен
	// байт, в одну запись помещаются с запасом, а push-сервисы и так не берут
	// больше четырёх килобайт.
	recordSize = 4096
	// saltLen/keyLen/nonceLen — размеры из RFC 8188: соль записи, ключ AES-128
	// и его nonce.
	saltLen  = 16
	keyLen   = 16
	nonceLen = 12
	// publicKeyLen — несжатый ключ P-256: 0x04 и две координаты по 32 байта.
	publicKeyLen = 65
	// headerLen — заголовок записи: соль, размер записи, длина keyid и сам keyid.
	headerLen = saltLen + 4 + 1 + publicKeyLen
	// overhead — сколько к открытому тексту добавляют разделитель заполнения и
	// тег AES-GCM. Ровно на столько запись длиннее сообщения.
	overhead = 1 + 16
)

// encrypt собирает тело push-запроса для подписки с ключом ua и секретом auth.
func encrypt(ua *ecdh.PublicKey, auth, plaintext []byte) ([]byte, error) {
	if len(plaintext)+overhead > recordSize {
		return nil, fmt.Errorf("сообщение %d байт — не влезает в запись %d", len(plaintext), recordSize)
	}
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encryptWith(ua, auth, plaintext, as, salt)
}

// encryptWith — то же самое с заданными одноразовым ключом и солью. Отдельно от
// encrypt ради тестов: случайность нужна в работе и мешает в проверке.
func encryptWith(ua *ecdh.PublicKey, auth, plaintext []byte, as *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	key, nonce, err := deriveKeys(as, ua, auth, salt)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	asPublic := as.PublicKey().Bytes()
	out := make([]byte, 0, headerLen+len(plaintext)+overhead)
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPublic)))
	out = append(out, asPublic...)

	// 0x02 — разделитель заполнения последней (и единственной) записи RFC 8188.
	// У записи, за которой шли бы другие, здесь стоял бы 0x01.
	record := append(append(make([]byte, 0, len(plaintext)+1), plaintext...), 0x02)
	return gcm.Seal(out, nonce, record, nil), nil
}

// deriveKeys выводит ключ и nonce записи из общего секрета ECDH — шаги из
// RFC 8291 §3.4 и RFC 8188 §2.2.
//
// Порядок ключей в key_info обязателен и не симметричен: сначала ключ браузера,
// потом наш. Перепутав их, мы получили бы ключ, который браузер не выведет, и
// уведомление молча не доехало бы.
func deriveKeys(as *ecdh.PrivateKey, ua *ecdh.PublicKey, auth, salt []byte) (key, nonce []byte, err error) {
	shared, err := as.ECDH(ua)
	if err != nil {
		return nil, nil, fmt.Errorf("ECDH с ключом подписки: %w", err)
	}

	// Секрет подписки (auth) — соль первого HKDF: без него общий секрет ECDH
	// вывел бы кто угодно, у кого есть открытый ключ подписки.
	prkKey, err := hkdf.Extract(sha256.New, shared, auth)
	if err != nil {
		return nil, nil, err
	}
	keyInfo := make([]byte, 0, len("WebPush: info")+1+publicKeyLen*2)
	keyInfo = append(keyInfo, "WebPush: info"...)
	keyInfo = append(keyInfo, 0)
	keyInfo = append(keyInfo, ua.Bytes()...)
	keyInfo = append(keyInfo, as.PublicKey().Bytes()...)
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(keyInfo), sha256.Size)
	if err != nil {
		return nil, nil, err
	}

	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, nil, err
	}
	key, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", keyLen)
	if err != nil {
		return nil, nil, err
	}
	nonce, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", nonceLen)
	if err != nil {
		return nil, nil, err
	}
	return key, nonce, nil
}
