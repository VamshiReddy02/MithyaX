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

// TestWebSocket_TwoClientsSignalThroughRoom proves two browsers can find
// each other through the gateway and exchange a full WebRTC signaling
// handshake (peers -> join -> offer -> answer -> ICE -> leave), now
// addressed by peer ID rather than blindly broadcast.
func TestWebSocket_TwoClientsSignalThroughRoom(t *testing.T) {
	_, wsURL := newWSTestServer(t)

	clientA := dialRoom(t, wsURL, "room-1")

	// A is alone, so it gets an empty peer roster.
	peersMsg := readMessage(t, clientA)
	if peersMsg.Type != websocket.TypePeers || len(peersMsg.Peers) != 0 {
		t.Fatalf("clientA got %+v, want empty TypePeers", peersMsg)
	}

	clientB := dialRoom(t, wsURL, "room-1")

	// B learns about A.
	bPeers := readMessage(t, clientB)
	if bPeers.Type != websocket.TypePeers || len(bPeers.Peers) != 1 {
		t.Fatalf("clientB got %+v, want one existing peer", bPeers)
	}
	aID := bPeers.Peers[0]

	// A learns B joined, and now knows B's ID to address messages to.
	joinMsg := readMessage(t, clientA)
	if joinMsg.Type != websocket.TypeJoin || joinMsg.From == "" {
		t.Fatalf("clientA got %+v, want TypeJoin with a From id", joinMsg)
	}
	bID := joinMsg.From

	// A sends an offer addressed to B; the gateway must stamp From itself.
	offer := websocket.Message{Type: websocket.TypeOffer, To: bID, Payload: json.RawMessage(`{"sdp":"fake-offer"}`)}
	if err := clientA.WriteJSON(offer); err != nil {
		t.Fatalf("clientA WriteJSON(offer): %v", err)
	}
	got := readMessage(t, clientB)
	if got.Type != websocket.TypeOffer || got.From != aID || string(got.Payload) != string(offer.Payload) {
		t.Fatalf("clientB got %+v, want offer from %s", got, aID)
	}

	// B answers back to A specifically.
	answer := websocket.Message{Type: websocket.TypeAnswer, To: aID, Payload: json.RawMessage(`{"sdp":"fake-answer"}`)}
	if err := clientB.WriteJSON(answer); err != nil {
		t.Fatalf("clientB WriteJSON(answer): %v", err)
	}
	got = readMessage(t, clientA)
	if got.Type != websocket.TypeAnswer || got.From != bID {
		t.Fatalf("clientA got %+v, want answer from %s", got, bID)
	}

	// ICE candidates flow the same addressed way.
	candidate := websocket.Message{Type: websocket.TypeICECandidate, To: bID, Payload: json.RawMessage(`{"candidate":"fake"}`)}
	if err := clientA.WriteJSON(candidate); err != nil {
		t.Fatalf("clientA WriteJSON(candidate): %v", err)
	}
	got = readMessage(t, clientB)
	if got.Type != websocket.TypeICECandidate || got.From != aID {
		t.Fatalf("clientB got %+v, want candidate from %s", got, aID)
	}

	// B hangs up, A is notified who left.
	clientB.Close()
	got = readMessage(t, clientA)
	if got.Type != websocket.TypeLeave || got.From != bID {
		t.Fatalf("clientA got %+v, want leave from %s", got, bID)
	}
}

// TestWebSocket_ThreeClientMesh proves a third participant joining a
// two-person room gets told about both existing peers, and both existing
// peers get told about the newcomer — the roster exchange a mesh call
// needs to open one connection per pair.
func TestWebSocket_ThreeClientMesh(t *testing.T) {
	_, wsURL := newWSTestServer(t)

	clientA := dialRoom(t, wsURL, "room-mesh")
	readMessage(t, clientA) // empty peers

	clientB := dialRoom(t, wsURL, "room-mesh")
	bPeers := readMessage(t, clientB) // peers: [A]
	aID := bPeers.Peers[0]

	bJoinAtA := readMessage(t, clientA) // join: B
	bID := bJoinAtA.From

	clientC := dialRoom(t, wsURL, "room-mesh")

	cPeers := readMessage(t, clientC)
	if cPeers.Type != websocket.TypePeers || len(cPeers.Peers) != 2 {
		t.Fatalf("clientC got %+v, want two existing peers", cPeers)
	}

	cJoinAtA := readMessage(t, clientA)
	if cJoinAtA.Type != websocket.TypeJoin {
		t.Fatalf("clientA got %+v, want TypeJoin for C", cJoinAtA)
	}
	cID := cJoinAtA.From

	cJoinAtB := readMessage(t, clientB)
	if cJoinAtB.Type != websocket.TypeJoin || cJoinAtB.From != cID {
		t.Fatalf("clientB got %+v, want TypeJoin for C (%s)", cJoinAtB, cID)
	}

	// A and B each independently offer to C — mesh means both establish
	// their own direct connection to the newcomer.
	offerFromA := websocket.Message{Type: websocket.TypeOffer, To: cID, Payload: json.RawMessage(`{"sdp":"from-a"}`)}
	if err := clientA.WriteJSON(offerFromA); err != nil {
		t.Fatalf("clientA WriteJSON(offer): %v", err)
	}
	offerFromB := websocket.Message{Type: websocket.TypeOffer, To: cID, Payload: json.RawMessage(`{"sdp":"from-b"}`)}
	if err := clientB.WriteJSON(offerFromB); err != nil {
		t.Fatalf("clientB WriteJSON(offer): %v", err)
	}

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		got := readMessage(t, clientC)
		if got.Type != websocket.TypeOffer {
			t.Fatalf("clientC got %+v, want an offer", got)
		}
		seen[got.From] = true
	}
	if !seen[aID] || !seen[bID] {
		t.Errorf("clientC should have received offers from both a (%s) and b (%s); seen = %v", aID, bID, seen)
	}
}

func TestWebSocket_ExplicitLeaveNotifiesPeer(t *testing.T) {
	_, wsURL := newWSTestServer(t)

	clientA := dialRoom(t, wsURL, "room-2")
	readMessage(t, clientA) // peers

	clientB := dialRoom(t, wsURL, "room-2")
	readMessage(t, clientB)         // peers
	readMessage(t, clientA)         // join

	if err := clientB.WriteJSON(websocket.Message{Type: websocket.TypeLeave}); err != nil {
		t.Fatalf("clientB WriteJSON(leave): %v", err)
	}

	got := readMessage(t, clientA)
	if got.Type != websocket.TypeLeave {
		t.Fatalf("clientA got type %q, want %q", got.Type, websocket.TypeLeave)
	}
}

func TestWebSocket_RoomFullRejectsOverCapacity(t *testing.T) {
	_, wsURL := newWSTestServer(t)

	for i := 0; i < 6; i++ {
		conn := dialRoom(t, wsURL, "room-3")
		readMessage(t, conn) // drain its peers message so it doesn't block anyone
	}

	conn, _, err := gorilla.DefaultDialer.Dial(wsURL+"?room=room-3", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected the 7th client's connection to be closed, got no error")
	}
}

func TestWebSocket_RoomsAreIsolated(t *testing.T) {
	_, wsURL := newWSTestServer(t)

	roomOneA := dialRoom(t, wsURL, "room-a")
	readMessage(t, roomOneA) // peers
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
