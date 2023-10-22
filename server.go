package pubsub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var ErrStoreLimitExceeded = errors.New("websockets store limit exceeded")
var BroadcastLimitExceededCloseMessage = websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "client broadcast message limit exceeded")

// Server is the websockets server struct containing m (a map[string] of map[*Store]bool entries).
type Server struct {
	sync.RWMutex

	ctx context.Context

	// Settings
	writeWait      time.Duration // Time allowed to write a message to the peer.
	pongWait       time.Duration // Time allowed to read the next pong message from the peer.
	pingPeriod     time.Duration // Send pings to peer with this period. Must be less than pongWait.
	maxMessageSize int64         // Maximum message size allowed from peer.

	// Upgrader
	upgrader websocket.Upgrader

	// map[string] of map[*Store]bool entries
	m map[string]map[*Store]bool
}

// Store stores a conn variable (the *websocket.Conn) and a sendCh variable (a buffered channel of outbound []byte messages).
type Store struct {
	sync.Mutex // write lock

	// Allow publishing and subscribing to the broadcast connection
	allowPub bool
	allowSub bool

	// The websocket connection.
	conn *websocket.Conn

	// Buffered channel of outbound message []byte data
	sendCh chan []byte

	// Limit the minimum delay between broadcasts for each connection (default is 0*time.Second - no limit)
	// Close the connection if broadcast limit is exceeded
	broadcastLimit time.Duration

	// Broadcast rate limit tracker
	lastBroadcastAt time.Time
}

// NewServer returns a new websockets server with default settings.
func NewServer(ctx context.Context) *Server {
	return NewServerWithSettings(
		ctx,
		defaultWriteWait,
		defaultPongWait,
		defaultPingPeriod,
		defaultMaxMessageSize,
	)
}

// NewServerWithSettings returns a new websockets server.
func NewServerWithSettings(
	ctx context.Context,
	writeWait time.Duration,
	pongWait time.Duration,
	pingPeriod time.Duration,
	maxMessageSize int64,
) *Server {
	s := &Server{
		ctx: ctx,

		upgrader: websocket.Upgrader{
			//ReadBufferSize:  1024,
			ReadBufferSize: 0,
			//WriteBufferSize: 1024,
			WriteBufferSize: 0,
			// TODO: Validate websocket connectionStores (possibly using CSRF)
			//CheckOrigin: func(r *http.Request) bool {
			//    return true
			//},
		},

		m: make(map[string]map[*Store]bool),

		writeWait:      writeWait,
		pongWait:       pongWait,
		pingPeriod:     pingPeriod,
		maxMessageSize: maxMessageSize,
	}
	go func() {
		<-s.ctx.Done()
		s.closeAll()
	}()
	return s
}

// HandlePubSub is a handler that creates a websocket connection with a channel string identifier.
// All messages sent via this websocket connection are broadcast to all other clients subscribed with this channel identifier.
// All broadcast messages sent by other clients using this identifier are received by this websocket connection.
func (s *Server) HandlePubSub(channel string, w http.ResponseWriter, r *http.Request) (err error) {
	return s.handle(channel, true, true, nil, defaultStoreLimit, defaultBroadcastLimit, w, r)
}

// HandlePubSubWithHeader is HandlePubSub but uses a custom header.
func (s *Server) HandlePubSubWithHeader(channel string, header http.Header, w http.ResponseWriter, r *http.Request) (err error) {
	return s.handle(channel, true, true, header, defaultStoreLimit, defaultBroadcastLimit, w, r)
}

// HandlePubSubWithHeaderAndLimits is HandlePubSub but uses a custom header and has store and/or broadcast limits.
// storeLimit is the maximum number of concurrent Stores (i.e. connections) that is allowed for each channel (default is 0 - no limit).
// broadcastLimit is the minimum delay required between broadcasts for each connection (default is 0*time.Second - no limit).
// The connection is closed if the broadcast limit is exceeded
func (s *Server) HandlePubSubWithHeaderAndLimits(channel string, header http.Header, storeLimit int64, broadcastLimit time.Duration, w http.ResponseWriter, r *http.Request) (err error) {
	return s.handle(channel, true, true, header, storeLimit, broadcastLimit, w, r)
}

// HandleSub is HandlePubSub but is Sub only (i.e. messages sent via this websocket connection are not broadcast to other subscribed clients).
func (s *Server) HandleSub(channel string, w http.ResponseWriter, r *http.Request) (err error) {
	return s.handle(channel, false, true, nil, defaultStoreLimit, defaultBroadcastLimit, w, r)
}

// HandleSubWithHeader is HandleSub but uses a custom header.
func (s *Server) HandleSubWithHeader(channel string, header http.Header, w http.ResponseWriter, r *http.Request) (err error) {
	return s.handle(channel, false, true, header, defaultStoreLimit, defaultBroadcastLimit, w, r)
}

// HandleSubWithHeaderAndLimits is HandleSub but uses a custom header and has store and/or broadcast limits.
// storeLimit is the maximum number of concurrent Stores (i.e. connections) that is allowed for each channel (default is 0 - no limit).
// broadcastLimit is the minimum delay required between broadcasts for each connection (default is 0*time.Second - no limit).
// The connection is closed if the broadcast limit is exceeded.
func (s *Server) HandleSubWithHeaderAndLimits(channel string, header http.Header, storeLimit int64, broadcastLimit time.Duration, w http.ResponseWriter, r *http.Request) (err error) {
	return s.handle(channel, false, true, header, storeLimit, broadcastLimit, w, r)
}

// HandlePub is HandlePub but is Pub only (i.e. broadcast messages sent by other clients are not received by this websocket connection).
func (s *Server) HandlePub(channel string, w http.ResponseWriter, r *http.Request) (err error) {
	return s.handle(channel, true, false, nil, defaultStoreLimit, defaultBroadcastLimit, w, r)
}

// HandlePubWithHeader is HandlePub but uses a custom header
func (s *Server) HandlePubWithHeader(channel string, header http.Header, w http.ResponseWriter, r *http.Request) (err error) {
	return s.handle(channel, true, false, header, defaultStoreLimit, defaultBroadcastLimit, w, r)
}

// HandlePubWithHeaderAndLimits is HandlePub but uses a custom header and has store and/or broadcast limits.
// storeLimit is the maximum number of concurrent Stores (i.e. connections) that is allowed for each channel (default is 0 - no limit).
// broadcastLimit is the minimum delay required between broadcasts for each connection (default is 0*time.Second - no limit).
// The connection is closed if the broadcast limit is exceeded.
func (s *Server) HandlePubWithHeaderAndLimits(channel string, header http.Header, storeLimit int64, broadcastLimit time.Duration, w http.ResponseWriter, r *http.Request) (err error) {
	return s.handle(channel, true, false, header, storeLimit, broadcastLimit, w, r)
}

func (s *Server) handle(
	channel string,
	allowPub bool,
	allowSub bool,
	header http.Header,
	storeLimit int64,
	broadcastLimit time.Duration,
	w http.ResponseWriter,
	r *http.Request,
) (err error) {
	if channel == "" {
		return fmt.Errorf("empty channel string")
	}

	conn, err := s.upgrader.Upgrade(w, r, header)
	if err != nil {
		return fmt.Errorf("websockets client initialisation (Upgrade) error: %s", err.Error())
	}

	// Create the Store
	st := &Store{
		allowPub:        allowPub,
		allowSub:        allowSub,
		conn:            conn,
		sendCh:          make(chan []byte, 256),
		broadcastLimit:  broadcastLimit,
		lastBroadcastAt: time.Now().UTC().Add(-broadcastLimit),
	}

	// Register the Store
	err = s.registerStore(channel, storeLimit, st)
	if err != nil {
		return err
	}

	// Start writing and reading messages
	go s.writeStoreMessages(st)
	return s.readAndBroadcastStoreMessages(channel, st)
}

func (s *Server) closeAll() {
	s.Lock()
	defer s.Unlock()

	for channel := range s.m {
		storeMap := s.m[channel]
		for st := range storeMap {
			s.deleteStoreFromMap(channel, st)
		}
	}
}

func (s *Server) deleteStoreFromMap(channel string, st *Store) {
	st.Lock()
	defer st.Unlock()

	close(st.sendCh)
	delete(s.m[channel], st)
	if len(s.m[channel]) == 0 {
		delete(s.m, channel)
	}
}

func (s *Server) registerStore(channel string, storeLimit int64, st *Store) error {
	s.Lock()
	defer s.Unlock()

	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	default:
	}

	storeMap := s.m[channel]

	// Limit number of stores for each channel to the storeLimit limit (if any)
	if storeLimit > 0 && int64(len(storeMap)) > storeLimit {
		return ErrStoreLimitExceeded
	}

	if storeMap == nil {
		storeMap = make(map[*Store]bool)
		s.m[channel] = storeMap
	}
	s.m[channel][st] = true
	return nil
}

func (s *Server) unregisterStore(channel string, st *Store) {
	s.Lock()
	defer s.Unlock()

	select {
	case <-s.ctx.Done():
		return
	default:
	}

	storeMap := s.m[channel]
	if storeMap != nil {
		if _, ok := storeMap[st]; ok {
			s.deleteStoreFromMap(channel, st)
		}
	}
}

func (s *Server) broadcastMessage(channel string, data []byte) {
	s.Lock()
	defer s.Unlock()

	select {
	case <-s.ctx.Done():
		return
	default:
	}

	storeMap := s.m[channel]
	for st := range storeMap {
		select {
		case st.sendCh <- data:
		default:
			s.deleteStoreFromMap(channel, st)
		}
	}
}

func (s *Server) readAndBroadcastStoreMessages(channel string, st *Store) (err error) {
	defer func() {
		s.unregisterStore(channel, st)

		st.Lock()
		st.conn.Close()
		st.Unlock()
	}()
	st.Lock()
	st.conn.SetReadLimit(s.maxMessageSize)
	st.conn.SetReadDeadline(time.Now().UTC().Add(s.pongWait))
	st.conn.SetPongHandler(func(string) error { st.conn.SetReadDeadline(time.Now().UTC().Add(s.pongWait)); return nil })
	st.Unlock()
	for {
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		default:
		}

		_, data, err := st.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(
				err,
				websocket.CloseGoingAway,
				websocketNormalClose,
				websocketNoStatusClose,
				websocketAbnormalClose,
			) {
				return fmt.Errorf("warning: websocket client unexpectedly closed the connection: %v", err)
			}
			break
		}

		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		default:
		}

		// Ignore unauthorised broadcast requests
		if !st.allowPub {
			continue
		}

		// Close connection if the broadcast limit (if any) is exceeded
		ok, err := st.broadcastLimitCheck()
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("store broadcast message limit exceeded - closing connection")
		}

		s.broadcastMessage(channel, data)
	}
	return nil
}

func (s *Server) writeStoreMessages(st *Store) {
	ticker := time.NewTicker(s.pingPeriod)
	defer func() {
		ticker.Stop()

		st.Lock()
		st.conn.Close()
		st.Unlock()
	}()

	for {
		select {
		case data, ok := <-st.sendCh:
			// Note that st.conn.WriteMessage() may not be called even though we call SetWriteDeadline() here
			st.Lock()
			st.conn.SetWriteDeadline(time.Now().UTC().Add(s.writeWait))
			st.Unlock()
			if !ok {
				st.Lock()
				st.conn.WriteMessage(websocket.CloseMessage, []byte{})
				st.Unlock()
				return // Note that returning here calls st.conn.Close() in the defer function above
			}

			// Do not stream messages from other connections to this connection if not permitted to do so
			if !st.allowSub {
				continue
			}

			st.Lock()
			err := st.conn.WriteMessage(websocket.TextMessage, data)
			st.Unlock()
			if err != nil {
				return // Note that returning here calls st.conn.Close() in the defer function above
			}
		case <-ticker.C:
			st.Lock()
			st.conn.SetWriteDeadline(time.Now().UTC().Add(s.writeWait))
			err := st.conn.WriteMessage(websocket.PingMessage, []byte{})
			st.Unlock()
			if err != nil {
				return // Note that returning here calls st.conn.Close() in the defer function above
			}
		}
	}
}

// broadcastLimitCheck closes the connection if broadcast limit is exceeded.
func (st *Store) broadcastLimitCheck() (ok bool, err error) {
	if st.broadcastLimit <= 0*time.Second {
		return true, nil
	}
	st.Lock()
	defer st.Unlock()

	nextAllowedTime := st.lastBroadcastAt.Add(st.broadcastLimit)
	if nextAllowedTime.After(time.Now().UTC()) {
		err = st.conn.WriteMessage(websocket.CloseMessage, BroadcastLimitExceededCloseMessage)
		if err != nil {
			return false, err
		}
		return false, nil
	}
	st.lastBroadcastAt = time.Now().UTC()
	return true, nil
}
