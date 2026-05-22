package ws

import (
	"sync"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/metrics"
)

const maxClients = 5000

// Hub maintains the set of active WebSocket clients and broadcasts messages to them.
type Hub struct {
	clients map[*Client]struct{}
	mu      sync.RWMutex
	log     zerolog.Logger
}

// NewHub creates an empty Hub.
func NewHub(log zerolog.Logger) *Hub {
	return &Hub{
		clients: make(map[*Client]struct{}),
		log:     log,
	}
}

// Register adds a client to the hub. Returns false if the hub is at capacity.
func (h *Hub) Register(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) >= maxClients {
		return false
	}
	h.clients[c] = struct{}{}
	metrics.WSClients.Set(float64(len(h.clients)))
	return true
}

// Unregister removes a client from the hub.
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
	metrics.WSClients.Set(float64(len(h.clients)))
}

// Broadcast sends msg to every registered client.
// Copies the client list under RLock, then sends without holding the lock.
func (h *Hub) Broadcast(msg []byte) {
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		select {
		case c.send <- msg:
		default:
			h.log.Warn().Str("remote", c.remoteAddr).Msg("ws send buffer full, message dropped")
		}
	}
}

// Len returns the current number of connected clients.
func (h *Hub) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
