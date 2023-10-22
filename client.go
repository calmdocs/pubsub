package pubsub

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client is the websockets client.
type Client struct {
	connectionLock sync.Mutex // stop concurrent client connect() calls
	readLock       sync.Mutex // gorilla/websocket does not allow concurrent reads
	writeLock      sync.Mutex // gorilla/websocket does not allow concurrent writes

	// Settings
	WriteWait        time.Duration // Time allowed to write a message to the peer.
	PongWait         time.Duration // Time allowed to read the next pong message from the peer.
	PingPeriod       time.Duration // Send pings to peer with this period. Must be less than PongWait.
	MaxMessageSize   int64         // Maximum message size allowed from peer.
	CloseGracePeriod time.Duration // Grace period after closing websocket.

	host     string
	header   http.Header
	isSecure bool
	path     string
	id       string

	conn                  *websocket.Conn
	Dialer                *websocket.Dialer
	isFirstConnectionDone bool
}

// NewClient creates a new websockets client.
func NewClient(
	host string,
	isSecure bool,
	path string,
	id string,
) *Client {
	return NewClientWithHeader(
		host,
		nil,
		isSecure,
		path,
		id,
	)
}

// NewClientWithBearerToken creates a new websockets client with a brearer token.
func NewClientWithBearerToken(
	host string,
	token string,
	isSecure bool,
	path string,
	id string,
) *Client {
	var header http.Header
	if token != "" {
		header = http.Header{}
		header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	}
	return NewClientWithHeader(
		host,
		header,
		isSecure,
		path,
		id,
	)
}

// NewClientWithHeader creates a new websockets client with a custom header.
func NewClientWithHeader(
	host string,
	header http.Header,
	isSecure bool,
	path string,
	id string,
) *Client {
	c := &Client{
		WriteWait:        defaultWriteWait,
		PongWait:         defaultPongWait,
		PingPeriod:       defaultPingPeriod,
		MaxMessageSize:   defaultMaxMessageSize,
		CloseGracePeriod: defaultCloseGracePeriod,

		host:     host,
		header:   header,
		isSecure: isSecure,
		path:     path,
		id:       id,

		conn:                  nil,
		Dialer:                websocket.DefaultDialer,
		isFirstConnectionDone: false,
	}

	// Lock until we connect
	c.readLock.Lock()
	c.writeLock.Lock()

	return c
}

// Pub creates a new websockets client.
// Pub publishes a message and then closes the client connection.
func Pub(
	ctx context.Context,
	host string,
	isSecure bool,
	path string,
	id string,
	message []byte,
) (err error) {
	return PubWithHeader(ctx, host, nil, isSecure, path, id, message)
}

// PubWithBearerToken creates a new websockets client with a brearer token.
// PubWithBearerToken publishes a message and then closes the client connection.
func PubWithBearerToken(
	ctx context.Context,
	host string,
	token string,
	isSecure bool,
	path string,
	id string,
	message []byte,
) (err error) {
	var header http.Header
	if token != "" {
		header = http.Header{}
		header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	}
	return PubWithHeader(ctx, host, header, isSecure, path, id, message)
}

// PubWithHeader creates a new websockets client with a custom header.
// PubWithHeader publishes a message and then closes the client connection.
func PubWithHeader(
	parentCtx context.Context,
	host string,
	header http.Header,
	isSecure bool,
	path string,
	id string,
	message []byte,
) (err error) {
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	c := NewClientWithHeader(
		host,
		header,
		isSecure,
		path,
		id,
	)
	err = c.connect(ctx)
	if err != nil {
		return err
	}
	defer c.conn.Close()
	err = c.WriteMessage(websocket.TextMessage, message)
	if err != nil {
		return err
	}
	err = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	select {
	case <-ctx.Done():
	case <-time.After(c.CloseGracePeriod):
	}
	return c.conn.Close()
}

// AllowInsecureConnections allows the client to make insecure connections
// (i.e. to servers with self-signed certificates).
func (c *Client) AllowInsecureConnections() {
	c.Dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
}

// ReadMessage reads messageType int and p []byte from the websocket connection.
func (c *Client) ReadMessage() (messageType int, p []byte, err error) {
	c.readLock.Lock()
	defer c.readLock.Unlock()

	return c.conn.ReadMessage()
}

// WriteMessage writes b []byte to the websocket connection.
func (c *Client) WriteMessage(messageType int, data []byte) (err error) {
	c.writeLock.Lock()
	defer c.writeLock.Unlock()

	c.conn.SetWriteDeadline(time.Now().Add(c.WriteWait))
	return c.conn.WriteMessage(messageType, data)
}

// WriteTextMessage writesa websocket.TextMessage b []byte message to the websocket connection.
func (c *Client) WriteTextMessage(data []byte) (err error) {
	return c.WriteMessage(websocket.TextMessage, data)
}

// WriteMessageSlice writes each set of bytes in the dataSlice [][]byte to one websocket connection.
func (c *Client) WriteMessageSlice(dataSlice [][]byte) (err error) {
	c.writeLock.Lock()
	defer c.writeLock.Unlock()

	c.conn.SetWriteDeadline(time.Now().Add(c.WriteWait))
	w, err := c.conn.NextWriter(websocket.TextMessage)
	if err != nil {
		return err
	}
	for i, data := range dataSlice {
		data = data
		if i > 0 {
			_, err = w.Write([]byte{'\n'})
			if err != nil {
				return err
			}
		}
		_, err = w.Write(data)
		if err != nil {
			return err
		}
	}
	return nil
}

// WriteJSON writes marshalls v interface{} as JSON and sends the json []byte to the websocket connection.
func (c *Client) WriteJSON(v interface{}) (err error) {
	c.writeLock.Lock()
	defer c.writeLock.Unlock()

	c.conn.SetWriteDeadline(time.Now().Add(c.WriteWait))
	return c.conn.WriteJSON(v)
}

func (c *Client) connect(ctx context.Context) (err error) {
	c.connectionLock.Lock()
	defer c.connectionLock.Unlock()

	// Unlock reads and writes after first connection
	if !c.isFirstConnectionDone {
		c.isFirstConnectionDone = true
		defer c.readLock.Unlock()
		defer c.writeLock.Unlock()
	}

	p := c.path
	if c.id != "" {
		p = c.path + "/" + c.id
	}

	wsScheme := "ws"
	if c.isSecure {
		wsScheme = "wss"
	}

	u := url.URL{Scheme: wsScheme, Host: c.host, Path: p}
	c.conn, _, err = c.Dialer.DialContext(ctx, u.String(), c.header)
	if err != nil {
		return fmt.Errorf("DialContext dial: %s", err.Error())
	}
	//defer c.conn.Close()

	return nil
}

// Start starts the websocket client.
func (c *Client) Start(parentCtx context.Context, messageFunc func(message []byte) (err error)) (err error) {
	ctx, cancel := context.WithCancel(parentCtx)

	// Create connection
	err = c.connect(ctx)
	if err != nil {
		return err
	}
	defer c.conn.Close()

	// Set connection variables
	c.conn.SetReadLimit(c.MaxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(c.PongWait))
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(c.PongWait)); return nil })

	// Read and process websocket messages
	messageErrCh := make(chan error, 1)
	go func() {
		defer close(messageErrCh)
		defer cancel()

		for {
			select {
			case <-ctx.Done():
				messageErrCh <- nil
				return
			default:
			}

			_, message, err := c.ReadMessage()
			if err != nil {
				messageErrCh <- fmt.Errorf("c.ReadMessage: %s", err.Error())
				return
			}

			if messageFunc != nil {
				err = messageFunc(message)
				if err != nil {
					messageErrCh <- fmt.Errorf("messageFunc: %s", err.Error())
					return
				}
			}
		}
	}()

	// Ping
	pingErrCh := make(chan error, 1)
	go func() {
		defer close(pingErrCh)
		defer cancel()

		ticker := time.NewTicker(c.PingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				pingErrCh <- nil
				return
			case <-ticker.C:
				err := c.WriteMessage(websocket.PingMessage, nil)
				if err != nil {
					pingErrCh <- fmt.Errorf("websocket.PingMessage: %s", err.Error())
					return
				}
			}
		}
	}()

	// Return any errors
	messageErr := <-messageErrCh
	if messageErr != nil {
		err = messageErr
	}
	pingErr := <-pingErrCh
	if err != nil && pingErr != nil {
		err = pingErr
	}
	return err
}
