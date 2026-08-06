package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/source/moexiss"
	"github.com/funding-service/backend/internal/storage"
)

// instrumentMeta describes a supported instrument for the /instruments endpoint.
type instrumentMeta struct {
	Symbol      string   `json:"symbol"`
	Description string   `json:"description"`
	Sources     []string `json:"sources"`
}

var instruments = []instrumentMeta{
	{Symbol: "USDRUBF", Description: "USD/RUB futures (MOEX)", Sources: []string{"moex_vwap", "cbr_official"}},
	{Symbol: "EURRUBF", Description: "EUR/RUB futures (MOEX)", Sources: []string{"moex_vwap", "cbr_official"}},
	{Symbol: "CNYRUBF", Description: "CNY/RUB futures (MOEX)", Sources: []string{"moex_vwap"}},
}

// NewRouter builds and returns the chi router for the HTTP API.
func NewRouter(store *storage.Store, botUsername string, allowedOrigin string, log zerolog.Logger, getSpecs func() map[string]moexiss.InstrumentSpec) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware(allowedOrigin))
	r.Use(zerologMiddleware(log))
	r.Use(maxBodyMiddleware(1 << 10)) // 1 KB limit

	userLimiter := newIPLimiter()

	globalAllSpecs.startWarmup()

	r.Get("/api/v1/instruments", handleInstruments)
	r.Get("/api/v1/specs", handleSpecs(getSpecs))
	r.Get("/api/v1/all-specs", handleAllSpecs)
	r.Get("/api/v1/prices", handlePrices)
	r.Get("/api/v1/swap-rates", handleSwapRates)
	r.Get("/api/v1/snapshots/recent", handleRecentSnapshots(store))

	// Сессия браузера: анонимная при создании, становится аккаунтом после /start в боте.
	r.With(rateLimitMiddleware(userLimiter, 5, time.Minute)).
		Post("/api/v1/session", handleCreateSession(store))

	auth := authMiddleware(store)
	r.With(auth).Get("/api/v1/me", handleMe())
	r.With(auth).Get("/api/v1/me/telegram-link", handleTelegramLink(store, botUsername))

	// Админские данные: «Журнал» и «Скорость». Вкладки скрыты во фронте, но
	// решает всё равно сервер — иначе их видел бы любой, кто знает адрес.
	r.With(auth, adminMiddleware).Get("/api/v1/cb-publications", handleCBPublications(store))
	r.With(auth, adminMiddleware).Get("/api/v1/cbr-race", handleCBRRace)

	return r
}

func handleSpecs(getSpecs func() map[string]moexiss.InstrumentSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, getSpecs())
	}
}

func handleInstruments(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, instruments)
}

func handleRecentSnapshots(store *storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 300
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
				limit = n
			}
		}
		rows, err := store.RecentSnapshots(r.Context(), limit)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, rows)
	}
}

func handleCBPublications(store *storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		days := 7
		if v := r.URL.Query().Get("days"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
				days = n
			}
		}
		rows, err := store.RecentCBPublications(r.Context(), days)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, rows)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func corsMiddleware(allowedOrigin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			if allowedOrigin != "*" {
				w.Header().Add("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func zerologMiddleware(log zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			log.Debug().
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Str("remote", r.RemoteAddr).
				Msg("http request")
		})
	}
}

func maxBodyMiddleware(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}
