package positions_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/funding-service/backend/internal/positions"
)

func TestGetAccessToken_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/client/tradersdiaries/oauth/v2/silent" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("device_id") != "test-device" {
			t.Errorf("missing device_id")
		}
		if r.Header.Get("Cookie") == "" {
			t.Errorf("missing Cookie header")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"accessToken":  "tok-access",
			"refreshToken": "tok-refresh",
		})
	}))
	defer srv.Close()

	c := positions.NewClientWithURLs(srv.URL, "https://api.tradersdiaries.com")
	token, err := c.GetAccessToken(context.Background(), "my-sso-session", "test-device")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "tok-access" {
		t.Errorf("want tok-access, got %s", token)
	}
}

func TestGetAccessToken_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{"error": true, "code": 1003})
	}))
	defer srv.Close()

	c := positions.NewClientWithURLs(srv.URL, "https://api.tradersdiaries.com")
	_, err := c.GetAccessToken(context.Background(), "bad-session", "device")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGetPositions_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prod/prop/get-positions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-Authorization") != "my-access-token" {
			t.Errorf("missing X-Authorization header")
		}
		if r.Method != http.MethodPost {
			t.Errorf("want POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{
				"symbol":      "VTBR",
				"exchange":    "Акции MOEX",
				"side":        "buy",
				"pos":         7,
				"profit":      -635.08,
				"profit_perc": -100.057,
				"date":        "2026-05-22",
				"time":        "14:25:36",
				"asset":       "RUR",
			},
		})
	}))
	defer srv.Close()

	c := positions.NewClientWithURLs("https://id-api.tradersdiaries.com", srv.URL)
	pos, err := c.GetPositions(context.Background(), "my-access-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pos) != 1 {
		t.Fatalf("want 1 position, got %d", len(pos))
	}
	if pos[0].Symbol != "VTBR" {
		t.Errorf("want VTBR, got %s", pos[0].Symbol)
	}
	if pos[0].Side != "buy" {
		t.Errorf("want buy, got %s", pos[0].Side)
	}
}

func TestGetPositions_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]any{})
	}))
	defer srv.Close()

	c := positions.NewClientWithURLs("https://id-api.tradersdiaries.com", srv.URL)
	pos, err := c.GetPositions(context.Background(), "tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pos) != 0 {
		t.Errorf("want 0 positions, got %d", len(pos))
	}
}
