package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/funding-service/backend/internal/storage"
)

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"Bearer abc123", "abc123"},
		{"bearer abc123", "abc123"}, // схема регистронезависима по RFC 7235
		{"Bearer   abc123  ", "abc123"},
		{"", ""},
		{"abc123", ""},       // без схемы — не токен
		{"Basic abc123", ""}, // чужая схема
		{"Bearer ", ""},      // пусто после схемы
		{"Bearer", ""},       // только схема
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
		if c.header != "" {
			r.Header.Set("Authorization", c.header)
		}
		if got := bearerToken(r); got != c.want {
			t.Errorf("bearerToken(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}

// Скрыть вкладки во фронте мало: без этого гейта журнал и гонку каналов читал бы
// любой, кто знает адрес эндпоинта.
func TestAdminMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name string
		user *storage.AuthUser
		want int
	}{
		{"админ проходит", &storage.AuthUser{UserID: 1, Linked: true, IsAdmin: true}, http.StatusOK},
		{"привязанный не-админ — 403", &storage.AuthUser{UserID: 2, Linked: true}, http.StatusForbidden},
		{"анонимная сессия — 403", &storage.AuthUser{UserID: 3}, http.StatusForbidden},
		{"без пользователя в контексте — 403", nil, http.StatusForbidden},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/cb-publications", nil)
			if c.user != nil {
				r = r.WithContext(context.WithValue(r.Context(), userCtxKey, *c.user))
			}
			w := httptest.NewRecorder()
			adminMiddleware(next).ServeHTTP(w, r)
			if w.Code != c.want {
				t.Errorf("status = %d, want %d", w.Code, c.want)
			}
		})
	}
}
