package pubsub

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

func TestClient(t *testing.T) {
	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()

	// Variables
	pubSubAuthToken := "abc"
	pubAuthToken := "abc123"
	subAuthToken := "abc123456"
	wsHost := "localhost:8003"
	isSecure := false
	wsPath := "/ws"
	id := "1"

	// Create mux router
	r := mux.NewRouter().StrictSlash(true)

	// Start websockets server with integer channels
	s := NewServer(parentCtx)
	r.HandleFunc(fmt.Sprintf("%s/{id:[0-9]+}", wsPath), func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		id, ok := vars["id"]
		if !ok {
			t.Fatalf("no websockets id")
		}

		// Auth
		bearerToken := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch bearerToken {
		case pubSubAuthToken:
			err := s.HandlePubSub(id, w, r)
			if err != nil {
				fmt.Println(err.Error())
				t.Fatalf("websockets error: %s", err.Error())
			}
		case pubAuthToken:
			err := s.HandlePub(id, w, r)
			if err != nil {
				fmt.Println(err.Error())
				t.Fatalf("websockets error: %s", err.Error())
			}
		case subAuthToken:
			err := s.HandleSub(id, w, r)
			if err != nil {
				fmt.Println(err.Error())
				t.Fatalf("websockets error: %s", err.Error())
			}
		default:
			t.Fatalf("auth failure: (actual: %s)", bearerToken)
		}
	})

	// Create http server
	httpServer := http.Server{
		Handler: r,
		Addr:    wsHost,
		//WriteTimeout: 15 * time.Second,
		//ReadTimeout:  15 * time.Second,
	}
	defer httpServer.Close()

	go func() {
		ctx, cancel := context.WithCancel(parentCtx)
		defer cancel()

		// Create subscription only websockets client
		// Wait for single pub and 10 successful client pub messages
		openClientPubCount := 0
		newClientPubCount := 0
		subClient := NewClientWithBearerToken(
			wsHost,
			subAuthToken,
			isSecure,
			wsPath,
			id,
		)
		go func() {
			err := subClient.Start(ctx, func(message []byte) (err error) {
				switch string(message) {
				case "abc":
					openClientPubCount += 1
				case "def":
					newClientPubCount += 1
				default:
					t.Fatalf("message error: %s", string(message))
				}
				if openClientPubCount > 10 && newClientPubCount >= 10 {
					httpServer.Close()
				}
				return nil
			})
			if err != nil {
				t.Fatalf("sub client start error: %s", err.Error())
			}
		}()

		// Send 1000 messages to the sub client - should not be broadcast
		for i := 0; i < 1000; i++ {
			err := subClient.WriteMessage(websocket.TextMessage, []byte("invalid"))
			if err != nil {
				t.Fatal(err)
			}
		}

		// Create publish only websockets client
		// Send messages with the open client
		pubClient := NewClientWithBearerToken(
			wsHost,
			pubAuthToken,
			isSecure,
			wsPath,
			id,
		)
		go func() {
			err := pubClient.Start(ctx, func(message []byte) (err error) {
				return fmt.Errorf("message should not be received using a pub client: %s", string(message))
			})
			if err != nil {
				t.Fatalf("pub client start error: %s", err.Error())
			}
		}()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:

				// Send pub message with open client
				err := pubClient.WriteMessage(websocket.TextMessage, []byte("abc"))
				if err != nil {
					t.Fatal(err)
				}

				// Send pub message with a new client
				err = PubWithBearerToken(
					ctx,
					wsHost,
					pubAuthToken,
					isSecure,
					wsPath,
					id,
					[]byte("def"),
				)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}()

	// Start http server
	err := httpServer.ListenAndServe()
	if err != nil {
		if !strings.Contains(err.Error(), "http: Server closed") {
			t.Fatal(err)
		}
	}
}
