package handlers_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"

	"github.com/vamshireddy02/mithyax/gateway/internal/handlers"
	"github.com/vamshireddy02/mithyax/gateway/internal/websocket"
)

func newWSTestServer(t *testing.T) (server *httptest.Server, wsURL string) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router.GET("/api/v1/ws", handlers.NewWebSocket(websocket.NewHub(), logger))

	server = httptest.NewServer(router)
	t.Cleanup(server.Close)

	wsURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/ws"
	return server, wsURL
}

func dialRoom(t *testing.T, wsURL, room string) *gorilla.Conn {
	t.Helper()
	conn, resp, err := gorilla.DefaultDialer.Dial(wsURL+"?room="+room, nil)
	if err != nil {
		t.Fatalf("dial failed: %v (resp: %v)", err, resp)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func readMessage(t *testing.T, conn *gorilla.Conn) websocket.Message {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg websocket.Message
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("ReadJSON: %v", err)
	}
	return msg
}

// TestWebSocket_TwoClientsSignalThroughRoom is the core proof that two
// browsers can find each other through the gateway and exchange a full
// WebRTC signaling handshake (join -> offer -> answer -> ICE -> leave).
func TestWebSocket_TwoClientsSignalThroughRoom(t *testing.T) {
	_, wsURL := newWSTestServer(t)

	clientA := dialRoom(t, wsURL, "room-1")
	clientB := dialRoom(t, wsURL, "room-1")

	// A learns B joined.
	joinMsg := readMessage(t, clientA)
	if joinMsg.Type != websocket.TypeJoin {
		t.Fatalf("clientA got type %q, want %q", joinMsg.Type, websocket.TypeJoin)
	}

	// A sends an offer, B should receive it verbatim.
	offer := websocket.Message{Type: websocket.TypeOffer, Payload: json.RawMessage(`{"sdp":"fake-offer"}`)}
	if err := clientA.WriteJSON(offer); err != nil {
		t.Fatalf("clientA WriteJSON(offer): %v", err)
	}
	got := readMessage(t, clientB)
	if got.Type != websocket.TypeOffer || string(got.Payload) != string(offer.Payload) {
		t.Fatalf("clientB got %+v, want %+v", got, offer)
	}

	// B answers, A should receive it.
	answer := websocket.Message{Type: websocket.TypeAnswer, Payload: json.RawMessage(`{"sdp":"fake-answer"}`)}
	if err := clientB.WriteJSON(answer); err != nil {
		t.Fatalf("clientB WriteJSON(answer): %v", err)
	}
	got = readMessage(t, clientA)
	if got.Type != websocket.TypeAnswer || string(got.Payload) != string(answer.Payload) {
		t.Fatalf("clientA got %+v, want %+v", got, answer)
	}

	// Either side can then trade ICE candidates.
	candidate := websocket.Message{Type: websocket.TypeICECandidate, Payload: json.RawMessage(`{"candidate":"fake"}`)}
	if err := clientA.WriteJSON(candidate); err != nil {
		t.Fatalf("clientA WriteJSON(candidate): %v", err)
	}
	got = readMessage(t, clientB)
	if got.Type != websocket.TypeICECandidate {
		t.Fatalf("clientB got type %q, want %q", got.Type, websocket.TypeICECandidate)
	}

	// B hangs up, A is notified.
	clientB.Close()
	got = readMessage(t, clientA)
	if got.Type != websocket.TypeLeave {
		t.Fatalf("clientA got type %q, want %q", got.Type, websocket.TypeLeave)
	}
}

func TestWebSocket_ExplicitLeaveNotifiesPeer(t *testing.T) {
	_, wsURL := newWSTestServer(t)

	clientA := dialRoom(t, wsURL, "room-2")
	clientB := dialRoom(t, wsURL, "room-2")
	readMessage(t, clientA) // join notification, not under test here

	if err := clientB.WriteJSON(websocket.Message{Type: websocket.TypeLeave}); err != nil {
		t.Fatalf("clientB WriteJSON(leave): %v", err)
	}

	got := readMessage(t, clientA)
	if got.Type != websocket.TypeLeave {
		t.Fatalf("clientA got type %q, want %q", got.Type, websocket.TypeLeave)
	}
}

func TestWebSocket_RoomFullRejectsThirdClient(t *testing.T) {
	_, wsURL := newWSTestServer(t)

	dialRoom(t, wsURL, "room-3")
	dialRoom(t, wsURL, "room-3")

	conn, _, err := gorilla.DefaultDialer.Dial(wsURL+"?room=room-3", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected the third client's connection to be closed, got no error")
	}
}

func TestWebSocket_RoomsAreIsolated(t *testing.T) {
	_, wsURL := newWSTestServer(t)

	roomOneA := dialRoom(t, wsURL, "room-a")
	dialRoom(t, wsURL, "room-b")

	roomOneA.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	_, _, err := roomOneA.ReadMessage()
	if err == nil {
		t.Fatal("clientA in room-a received a message from an unrelated room-b join")
	}
}

func TestWebSocket_RoomRequired(t *testing.T) {
	server, _ := newWSTestServer(t)

	resp, err := http.Get(server.URL + "/api/v1/ws")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}
