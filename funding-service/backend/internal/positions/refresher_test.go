package positions_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/funding-service/backend/internal/positions"
	"github.com/rs/zerolog"
)

func TestRefresher_GetToken_Empty(t *testing.T) {
	log := zerolog.Nop()
	r := positions.NewRefresher(positions.New(), log)
	if tok := r.Token(); tok != "" {
		t.Errorf("expected empty token, got %q", tok)
	}
}

func TestRefresher_Reload_FetchesToken(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]string{
			"accessToken":  "fresh-token",
			"refreshToken": "rt",
		})
	}))
	defer srv.Close()

	log := zerolog.Nop()
	client := positions.NewClientWithURLs(srv.URL, "https://api.tradersdiaries.com")
	r := positions.NewRefresher(client, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.Reload("my-sso", "my-device")
	go r.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.Token() == "fresh-token" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if r.Token() != "fresh-token" {
		t.Errorf("expected fresh-token, got %q", r.Token())
	}
	if calls.Load() == 0 {
		t.Error("expected at least one call to silent endpoint")
	}
}
