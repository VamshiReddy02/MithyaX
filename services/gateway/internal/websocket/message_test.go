package websocket_test

import (
	"encoding/json"
	"testing"

	"github.com/vamshireddy02/mithyax/gateway/internal/websocket"
)

func TestMessage_RoundTrip(t *testing.T) {
	original := websocket.Message{
		Type:    websocket.TypeOffer,
		From:    "a",
		To:      "b",
		Payload: json.RawMessage(`{"sdp":"fake-sdp"}`),
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var decoded websocket.Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if decoded.Type != original.Type {
		t.Errorf("Type = %q, want %q", decoded.Type, original.Type)
	}
	if decoded.From != original.From {
		t.Errorf("From = %q, want %q", decoded.From, original.From)
	}
	if decoded.To != original.To {
		t.Errorf("To = %q, want %q", decoded.To, original.To)
	}
	if string(decoded.Payload) != string(original.Payload) {
		t.Errorf("Payload = %s, want %s", decoded.Payload, original.Payload)
	}
}

func TestMessage_PeersRoundTrip(t *testing.T) {
	original := websocket.Message{Type: websocket.TypePeers, Peers: []string{"a", "b"}}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var decoded websocket.Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if len(decoded.Peers) != 2 || decoded.Peers[0] != "a" || decoded.Peers[1] != "b" {
		t.Errorf("Peers = %v, want [a b]", decoded.Peers)
	}
}

func TestMessage_TypeValues(t *testing.T) {
	cases := map[websocket.Type]string{
		websocket.TypePeers:        "peers",
		websocket.TypeJoin:         "join",
		websocket.TypeOffer:        "offer",
		websocket.TypeAnswer:       "answer",
		websocket.TypeICECandidate: "ice-candidate",
		websocket.TypeLeave:        "leave",
	}

	for got, want := range cases {
		if string(got) != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestMessage_UnmarshalWithoutPayload(t *testing.T) {
	var msg websocket.Message
	if err := json.Unmarshal([]byte(`{"type":"join"}`), &msg); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if msg.Type != websocket.TypeJoin {
		t.Errorf("Type = %q, want %q", msg.Type, websocket.TypeJoin)
	}
	if msg.Payload != nil {
		t.Errorf("Payload = %s, want nil", msg.Payload)
	}
}
