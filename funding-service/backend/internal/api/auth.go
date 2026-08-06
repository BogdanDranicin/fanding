package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/funding-service/backend/internal/storage"
)

type ctxKey int

const userCtxKey ctxKey = iota

// bearerToken достаёт токен сессии из заголовка Authorization.
// Токен намеренно НЕ читается из query: адреса попадают в логи прокси и в
// history браузера, а этот токен — единственный ключ от аккаунта.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// authMiddleware кладёт владельца сессии в контекст запроса. Неизвестный или
// отсутствующий токен — 401: фронт на это создаёт новую сессию и повторяет.
func authMiddleware(store *storage.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			if token == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			user, err := store.SessionUser(r.Context(), token)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userCtxKey, user)))
		})
	}
}

// adminMiddleware пускает дальше только админов. Ставится ПОСЛЕ authMiddleware.
// Без него скрытые во фронте вкладки «Журнал» и «Скорость» оставались бы
// доступны любому, кто знает адрес их эндпоинтов.
func adminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := userFromContext(r.Context())
		if !ok || !user.IsAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func userFromContext(ctx context.Context) (storage.AuthUser, bool) {
	u, ok := ctx.Value(userCtxKey).(storage.AuthUser)
	return u, ok
}

func handleCreateSession(store *storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, err := store.CreateSession(r.Context())
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"token": token})
	}
}

// handleMe отдаёт состояние текущей сессии: привязан ли Telegram и админ ли это.
// Фронт спрашивает её при загрузке и при возврате фокуса на вкладку — так сайт
// узнаёт о привязке, сделанной только что в Telegram, без перезагрузки.
func handleMe() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := userFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"linked":            user.Linked,
			"is_admin":          user.IsAdmin,
			"telegram_username": user.Username,
		})
	}
}

// handleTelegramLink возвращает deep-link для привязки текущей сессии к чату.
func handleTelegramLink(store *storage.Store, botUsername string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if botUsername == "" {
			http.Error(w, "bot not configured", http.StatusServiceUnavailable)
			return
		}
		user, ok := userFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		token, err := store.EnsureLinkToken(r.Context(), user.UserID)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"url":      fmt.Sprintf("https://t.me/%s?start=%s", botUsername, token),
			"linked":   user.Linked,
			"is_admin": user.IsAdmin,
		})
	}
}
