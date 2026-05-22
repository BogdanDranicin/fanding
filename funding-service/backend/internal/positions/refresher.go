package positions

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

const refreshInterval = 9 * time.Minute

// Refresher периодически обновляет accessToken и хранит его в памяти.
type Refresher struct {
	client     *Client
	log        zerolog.Logger
	mu         sync.RWMutex
	token      string
	ssoSession string
	deviceID   string
	reloadCh   chan struct{}
}

// NewRefresher создаёт Refresher. Вызови Run(ctx) в горутине для запуска цикла.
func NewRefresher(client *Client, log zerolog.Logger) *Refresher {
	return &Refresher{
		client:   client,
		log:      log,
		reloadCh: make(chan struct{}, 1),
	}
}

// Reload устанавливает новые учётные данные и немедленно запускает обновление токена.
func (r *Refresher) Reload(ssoSession, deviceID string) {
	r.mu.Lock()
	r.ssoSession = ssoSession
	r.deviceID = deviceID
	r.mu.Unlock()
	select {
	case r.reloadCh <- struct{}{}:
	default:
	}
}

// Token возвращает последний полученный accessToken. Пустая строка — не настроено.
func (r *Refresher) Token() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.token
}

// Run блокирует до отмены ctx, периодически обновляя токен.
func (r *Refresher) Run(ctx context.Context) {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.reloadCh:
			r.refresh(ctx)
		case <-ticker.C:
			r.mu.RLock()
			configured := r.ssoSession != ""
			r.mu.RUnlock()
			if configured {
				r.refresh(ctx)
			}
		}
	}
}

func (r *Refresher) refresh(ctx context.Context) {
	r.mu.RLock()
	sso := r.ssoSession
	dev := r.deviceID
	r.mu.RUnlock()

	if sso == "" {
		return
	}

	token, err := r.client.GetAccessToken(ctx, sso, dev)
	if err != nil {
		r.log.Warn().Err(err).Msg("positions: token refresh failed")
		return
	}

	r.mu.Lock()
	r.token = token
	r.mu.Unlock()
	r.log.Debug().Msg("positions: access token refreshed")
}
