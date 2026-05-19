package ws

import (
	"sync"

	"github.com/rs/zerolog"
)

// Hub maintains the set of active WebSocket clients and broadcasts messages to them.
type Hub struct {
	clients map[*Client]struct{}
	mu      sync.Mutex
	log     zerolog.Logger
}

// NewHub creates an empty Hub.
func NewHub(log zerolog.Logger) *Hub {
	return &Hub{
		clients: make(map[*Client]struct{}),
		log:     log,
	}
}

// Register adds a client to the hub.
func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
}

// Unregister removes a client from the hub.
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
}

// Broadcast sends msg to every registered client.
// Slow clients whose send buffer is full are skipped with a warning — they never
// block or slow down the broadcast for other clients.
func (h *Hub) Broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- msg:
		default:
			h.log.Warn().Str("remote", c.remoteAddr).Msg("ws send buffer full, message dropped")
		}
	}
}

// Len returns the current number of connected clients.
func (h *Hub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}
