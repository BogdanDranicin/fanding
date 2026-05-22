package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/storage"
)

// instrumentMeta describes a supported instrument for the /instruments endpoint.
type instrumentMeta struct {
	Symbol      string   `json:"symbol"`
	Description string   `json:"description"`
	Sources     []string `json:"sources"`
}

var instruments = []instrumentMeta{
	{Symbol: "USDRUBF", Description: "USD/RUB futures (MOEX)", Sources: []string{"moex_vwap", "cbr_official", "forex"}},
	{Symbol: "EURRUBF", Description: "EUR/RUB futures (MOEX)", Sources: []string{"moex_vwap", "cbr_official", "forex"}},
	{Symbol: "CNYRUBF", Description: "CNY/RUB futures (MOEX)", Sources: []string{"moex_vwap"}},
}

// NewRouter builds and returns the chi router for the HTTP API.
func NewRouter(store *storage.Store, botUsername string, allowedOrigin string, log zerolog.Logger) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware(allowedOrigin))
	r.Use(zerologMiddleware(log))
	r.Use(maxBodyMiddleware(1 << 10)) // 1 KB limit

	r.Get("/api/v1/instruments", handleInstruments)
	r.Get("/api/v1/snapshots/recent", handleRecentSnapshots(store))
	r.Get("/api/v1/cb-publications", handleCBPublications(store))
	r.Post("/api/v1/users", handleCreateUser(store))
	r.Get("/api/v1/users/{id}/telegram-link", handleTelegramLink(store, botUsername))

	return r
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

func handleCreateUser(store *storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec, err := store.CreateUser(r.Context())
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":    rec.ID,
			"token": rec.LinkToken,
		})
	}
}

func handleTelegramLink(store *storage.Store, botUsername string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if botUsername == "" {
			http.Error(w, "bot not configured", http.StatusServiceUnavailable)
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		token, linked, err := store.UserByID(r.Context(), id)
		if err != nil {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"url":    fmt.Sprintf("https://t.me/%s?start=%s", botUsername, token),
			"linked": linked,
		})
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
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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
