package pubsub

import (
	"time"

	"github.com/gorilla/websocket"
)

const (
	BYTE     = 1 << (iota * 10) // = 1 << 0
	KILOBYTE                    // = 1 << 10
	MEGABYTE                    // = 1 << 20
	GIGABYTE                    // = 1 << 30

	// websocket close error codes.
	websocketNormalClose   = 1000
	websocketNoStatusClose = 1005
	websocketAbnormalClose = 1006

	// Time allowed to write a message to the peer.
	defaultWriteWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	defaultPongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	defaultPingPeriod = (defaultPongWait * 9) / 10

	// Maximum message size allowed from peer.
	defaultMaxMessageSize = 2 * MEGABYTE

	// Pub grace period after closing websocket.
	defaultCloseGracePeriod = 100 * time.Millisecond

	// Maximum number of concurrent Stores (i.e. connections) that is allowed for each channel.
	// Default is no limit.
	defaultStoreLimit = int64(0)

	// Minimum delay required between broadcasts for each connection.
	// Default is no limit.
	defaultBroadcastLimit = 0 * time.Second

	// Gorilla Message types.
	TextMessage   = websocket.TextMessage
	BinaryMessage = websocket.BinaryMessage
	CloseMessage  = websocket.CloseMessage
	PingMessage   = websocket.PingMessage
	PongMessage   = websocket.PongMessage
)
