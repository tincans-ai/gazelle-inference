package wsw

import (
	"sync"

	"github.com/gorilla/websocket"
)

// WSWrapper is a wrapper around a websocket connection that provides a mutex
// This is necessary to prevent concurrent writes to the websocket connection
type WSWrapper struct {
	*websocket.Conn
	mu sync.Mutex
}

// WriteJSONConcurrent is a wrapper around WriteJSON that uses a mutex to prevent concurrent writes
func (ws *WSWrapper) WriteJSONConcurrent(msg any) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.WriteJSON(msg)
}

// WriteMessageConcurrent is a wrapper around WriteMessage that uses a mutex to prevent concurrent writes
func (ws *WSWrapper) WriteMessageConcurrent(messageType int, data []byte) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.WriteMessage(messageType, data)
}

func NewWSWrapper(c *websocket.Conn) *WSWrapper {
	return &WSWrapper{
		Conn: c,
		mu:   sync.Mutex{},
	}
}
