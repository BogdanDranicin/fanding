package telegram

import (
	"net/http"
	"testing"
)

func TestProxyClientSchemeDefaultsToHTTP(t *testing.T) {
	cases := []struct {
		raw      string
		wantHost string // host:port the proxy URL should resolve to
	}{
		{"gMohPU:zMGpPy@213.139.222.80:9598", "213.139.222.80:9598"},
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
	if got := proxyHost("gMohPU:zMGpPy@213.139.222.80:9598"); got != "213.139.222.80:9598" {
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
