package telegram

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestProxyClientSchemeDefaultsToHTTP(t *testing.T) {
	cases := []struct {
		raw      string
		wantHost string // host:port the proxy URL should resolve to
	}{
		{"user:pass@198.51.100.7:9598", "198.51.100.7:9598"},
		{"http://user:pass@1.2.3.4:9700", "1.2.3.4:9700"},
		{"socks5://user:pass@1.2.3.5:9677", "1.2.3.5:9677"},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			client, err := proxyClient(c.raw)
			if err != nil {
				t.Fatalf("proxyClient(%q) error: %v", c.raw, err)
			}
			tr, ok := client.Transport.(*http.Transport)
			if !ok || tr.Proxy == nil {
				t.Fatalf("expected transport with proxy set")
			}
			// Resolve the proxy URL the transport would use for a sample request.
			req, _ := http.NewRequest(http.MethodGet, "https://api.telegram.org/", nil)
			u, err := tr.Proxy(req)
			if err != nil {
				t.Fatalf("proxy resolve error: %v", err)
			}
			if u == nil || u.Host != c.wantHost {
				t.Fatalf("proxy host = %v, want %s", u, c.wantHost)
			}
		})
	}
}

func TestProxyHostStripsCredentials(t *testing.T) {
	if got := proxyHost("user:pass@198.51.100.7:9598"); got != "198.51.100.7:9598" {
		t.Errorf("proxyHost = %q, want host without creds", got)
	}
	if got := proxyHost("http://1.2.3.4:9700"); got != "http://1.2.3.4:9700" {
		t.Errorf("proxyHost = %q, want unchanged when no creds", got)
	}
}

func TestNonEmpty(t *testing.T) {
	got := nonEmpty([]string{" ", "a", "", "  b "})
	if len(got) != 2 || got[0] != "a" || got[1] != "  b " {
		t.Errorf("nonEmpty = %#v, want [a, '  b ']", got)
	}
}

func TestHideTokenRemovesTokenFromError(t *testing.T) {
	const token = "111111111:TEST-token-not-a-real-one"
	err := errors.New(`Post "https://api.telegram.org/bot` + token + `/getMe": dial tcp: connection refused`)

	got := hideToken(err, token).Error()
	if strings.Contains(got, token) {
		t.Fatalf("токен остался в тексте ошибки: %s", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Errorf("причина ошибки потерялась: %s", got)
	}

	wrapped := fmt.Errorf("all telegram proxies failed: %w", hideToken(err, token))
	if strings.Contains(wrapped.Error(), token) {
		t.Errorf("токен просочился через обёртку: %s", wrapped.Error())
	}

	plain := errors.New("no usable proxy in TELEGRAM_PROXY_URL")
	if hideToken(plain, token) != plain {
		t.Error("ошибка без токена должна возвращаться как есть")
	}
	if hideToken(nil, token) != nil {
		t.Error("nil должен оставаться nil")
	}
}

func TestHasWebhookDetectsHijack(t *testing.T) {
	if hasWebhook(tgbotapi.WebhookInfo{}) {
		t.Error("пустой URL — webhook не выставлен")
	}
	if hasWebhook(tgbotapi.WebhookInfo{URL: "   "}) {
		t.Error("пробелы в URL — webhook не выставлен")
	}
	if !hasWebhook(tgbotapi.WebhookInfo{URL: "https://evil.example/hook"}) {
		t.Error("чужой webhook должен быть замечен")
	}
}
