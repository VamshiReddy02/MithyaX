package websocket_test

import (
	"sync"
	"testing"

	"github.com/vamshireddy02/mithyax/gateway/internal/websocket"
)

// fakePeer satisfies the hub's unexported peer interface structurally, so
// hub behavior can be tested without a real network connection.
type fakePeer struct {
	mu       sync.Mutex
	received []websocket.Message
}

func (f *fakePeer) Send(msg websocket.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.received = append(f.received, msg)
}

func (f *fakePeer) messages() []websocket.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]websocket.Message, len(f.received))
	copy(out, f.received)
	return out
}

func TestHub_JoinNotifiesExistingPeer(t *testing.T) {
	hub := websocket.NewHub()
	a := &fakePeer{}
	b := &fakePeer{}

	if err := hub.Join("room-1", a); err != nil {
		t.Fatalf("Join(a) error = %v", err)
	}
	if err := hub.Join("room-1", b); err != nil {
		t.Fatalf("Join(b) error = %v", err)
	}

	aMsgs := a.messages()
	if len(aMsgs) != 1 || aMsgs[0].Type != websocket.TypeJoin {
		t.Errorf("a.messages() = %+v, want one TypeJoin message", aMsgs)
	}
	if len(b.messages()) != 0 {
		t.Errorf("b.messages() = %+v, want none (b shouldn't see its own join)", b.messages())
	}
}

func TestHub_JoinRejectsThirdPeer(t *testing.T) {
	hub := websocket.NewHub()
	a, b, c := &fakePeer{}, &fakePeer{}, &fakePeer{}

	mustJoin(t, hub, "room-1", a)
	mustJoin(t, hub, "room-1", b)

	if err := hub.Join("room-1", c); err != websocket.ErrRoomFull {
		t.Fatalf("Join(c) error = %v, want ErrRoomFull", err)
	}
}

func TestHub_BroadcastExcludesSender(t *testing.T) {
	hub := websocket.NewHub()
	a, b := &fakePeer{}, &fakePeer{}

	mustJoin(t, hub, "room-1", a)
	mustJoin(t, hub, "room-1", b)
	aBaseline := len(a.messages()) // a already got a TypeJoin when b joined

	offer := websocket.Message{Type: websocket.TypeOffer}
	hub.Broadcast("room-1", a, offer)

	bMsgs := b.messages()
	if len(bMsgs) != 1 || bMsgs[0].Type != websocket.TypeOffer {
		t.Errorf("b.messages() = %+v, want one TypeOffer message", bMsgs)
	}
	if len(a.messages()) != aBaseline {
		t.Errorf("a.messages() = %+v, want no new messages (sender shouldn't receive its own broadcast)", a.messages())
	}
}

func TestHub_LeaveNotifiesRemainingPeer(t *testing.T) {
	hub := websocket.NewHub()
	a, b := &fakePeer{}, &fakePeer{}

	mustJoin(t, hub, "room-1", a)
	mustJoin(t, hub, "room-1", b)

	hub.Leave("room-1", a)

	bMsgs := b.messages()
	if len(bMsgs) != 1 || bMsgs[0].Type != websocket.TypeLeave {
		t.Errorf("b.messages() = %+v, want one TypeLeave message", bMsgs)
	}
}

func TestHub_LeaveEmptiesRoom(t *testing.T) {
	hub := websocket.NewHub()
	a, b := &fakePeer{}, &fakePeer{}

	mustJoin(t, hub, "room-1", a)
	hub.Leave("room-1", a)

	// The room should be gone, not stuck reporting full.
	if err := hub.Join("room-1", b); err != nil {
		t.Fatalf("Join(b) after room emptied: error = %v, want nil", err)
	}
}

func TestHub_RoomsAreIndependent(t *testing.T) {
	hub := websocket.NewHub()
	a, b := &fakePeer{}, &fakePeer{}

	mustJoin(t, hub, "room-1", a)
	mustJoin(t, hub, "room-2", b)

	hub.Broadcast("room-1", a, websocket.Message{Type: websocket.TypeOffer})

	if len(b.messages()) != 0 {
		t.Errorf("b.messages() = %+v, want none (different room)", b.messages())
	}
}

func mustJoin(t *testing.T, hub *websocket.Hub, room string, p interface{ Send(websocket.Message) }) {
	t.Helper()
	if err := hub.Join(room, p); err != nil {
		t.Fatalf("Join(%q) error = %v", room, err)
	}
}
