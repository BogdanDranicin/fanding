package ws

import (
	"context"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"
)

const sendBufSize = 64

// Client represents a connected WebSocket peer.
type Client struct {
	conn       *websocket.Conn
	send       chan []byte // buffered outgoing message queue
	hub        *Hub
	remoteAddr string
	log        zerolog.Logger
}

// NewClient creates a Client from an accepted WebSocket connection.
func NewClient(conn *websocket.Conn, hub *Hub, remoteAddr string, log zerolog.Logger) *Client {
	return &Client{
		conn:       conn,
		send:       make(chan []byte, sendBufSize),
		hub:        hub,
		remoteAddr: remoteAddr,
		log:        log.With().Str("remote", remoteAddr).Logger(),
	}
}

// WritePump reads from the send channel and forwards messages to the WebSocket.
// Returns when ctx is cancelled or a write error occurs.
func (c *Client) WritePump(ctx context.Context) {
	defer c.conn.Close(websocket.StatusNormalClosure, "")
	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			if err := c.conn.Write(ctx, websocket.MessageBinary, msg); err != nil {
				c.log.Debug().Err(err).Msg("ws write error")
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// ReadPump drains incoming messages from the WebSocket.
// coder/websocket handles ping/pong automatically; this loop handles future
// client commands and ensures the read side stays active for clean close detection.
// Returns when the connection closes or ctx is cancelled.
func (c *Client) ReadPump(ctx context.Context) {
	for {
		_, _, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
		// incoming messages are reserved for future client commands
	}
}
