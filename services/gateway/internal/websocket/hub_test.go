package websocket_test

import (
	"sort"
	"sync"
	"testing"

	"github.com/vamshireddy02/mithyax/gateway/internal/websocket"
)

// fakePeer satisfies the hub's unexported peer interface structurally, so
// hub behavior can be tested without a real network connection.
type fakePeer struct {
	id       string
	mu       sync.Mutex
	received []websocket.Message
}

func newFakePeer(id string) *fakePeer {
	return &fakePeer{id: id}
}

func (f *fakePeer) ID() string {
	return f.id
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

func TestHub_JoinReturnsExistingPeerIDs(t *testing.T) {
	hub := websocket.NewHub()
	a := newFakePeer("a")
	b := newFakePeer("b")
	c := newFakePeer("c")

	mustJoin(t, hub, "room-1", a)
	mustJoin(t, hub, "room-1", b)

	existing := mustJoin(t, hub, "room-1", c)
	sort.Strings(existing)
	if len(existing) != 2 || existing[0] != "a" || existing[1] != "b" {
		t.Errorf("Join(c) existing = %v, want [a b]", existing)
	}
}

func TestHub_JoinNotifiesExistingPeersWithNewID(t *testing.T) {
	hub := websocket.NewHub()
	a := newFakePeer("a")
	b := newFakePeer("b")

	mustJoin(t, hub, "room-1", a)
	mustJoin(t, hub, "room-1", b)

	aMsgs := a.messages()
	if len(aMsgs) != 1 || aMsgs[0].Type != websocket.TypeJoin || aMsgs[0].From != "b" {
		t.Errorf("a.messages() = %+v, want one TypeJoin from b", aMsgs)
	}
	if len(b.messages()) != 0 {
		t.Errorf("b.messages() = %+v, want none (b shouldn't see its own join)", b.messages())
	}
}

func TestHub_JoinRejectsRoomAtCapacity(t *testing.T) {
	hub := websocket.NewHub()
	// maxClientsPerRoom is 6; fill it, then the 7th should be rejected.
	for i := 0; i < 6; i++ {
		mustJoin(t, hub, "room-1", newFakePeer(string(rune('a'+i))))
	}

	if _, err := hub.Join("room-1", newFakePeer("overflow")); err != websocket.ErrRoomFull {
		t.Fatalf("Join() error = %v, want ErrRoomFull", err)
	}
}

func TestHub_RouteDeliversToExactTarget(t *testing.T) {
	hub := websocket.NewHub()
	a, b, c := newFakePeer("a"), newFakePeer("b"), newFakePeer("c")

	mustJoin(t, hub, "room-1", a)
	mustJoin(t, hub, "room-1", b)
	mustJoin(t, hub, "room-1", c)
	bBaseline := len(b.messages()) // b already got a TypeJoin when c joined

	offer := websocket.Message{Type: websocket.TypeOffer, From: "a", To: "c", Payload: nil}
	if err := hub.Route("room-1", offer); err != nil {
		t.Fatalf("Route() error = %v", err)
	}

	cMsgs := c.messages()
	if len(cMsgs) != 1 || cMsgs[0].Type != websocket.TypeOffer || cMsgs[0].From != "a" {
		t.Errorf("c.messages() = %+v, want one offer from a", cMsgs)
	}
	if len(b.messages()) != bBaseline {
		t.Errorf("b.messages() = %+v, want no new messages (message was addressed to c, not b)", b.messages())
	}
}

func TestHub_RouteToUnknownPeerFails(t *testing.T) {
	hub := websocket.NewHub()
	a := newFakePeer("a")
	mustJoin(t, hub, "room-1", a)

	err := hub.Route("room-1", websocket.Message{Type: websocket.TypeOffer, To: "ghost"})
	if err != websocket.ErrPeerNotFound {
		t.Fatalf("Route() error = %v, want ErrPeerNotFound", err)
	}
}

func TestHub_LeaveNotifiesRemainingPeersWithLeftID(t *testing.T) {
	hub := websocket.NewHub()
	a, b, c := newFakePeer("a"), newFakePeer("b"), newFakePeer("c")

	mustJoin(t, hub, "room-1", a)
	mustJoin(t, hub, "room-1", b)
	mustJoin(t, hub, "room-1", c)

	hub.Leave("room-1", b)

	for _, p := range []*fakePeer{a, c} {
		msgs := p.messages()
		last := msgs[len(msgs)-1]
		if last.Type != websocket.TypeLeave || last.From != "b" {
			t.Errorf("%s last message = %+v, want TypeLeave from b", p.id, last)
		}
	}
}

func TestHub_LeaveEmptiesRoom(t *testing.T) {
	hub := websocket.NewHub()
	a, b := newFakePeer("a"), newFakePeer("b")

	mustJoin(t, hub, "room-1", a)
	hub.Leave("room-1", a)

	// The room should be gone, not stuck reporting full or remembering a.
	existing := mustJoin(t, hub, "room-1", b)
	if len(existing) != 0 {
		t.Errorf("Join(b) existing = %v, want none (room was emptied)", existing)
	}
}

func TestHub_RoomsAreIndependent(t *testing.T) {
	hub := websocket.NewHub()
	a, b := newFakePeer("a"), newFakePeer("b")

	mustJoin(t, hub, "room-1", a)
	mustJoin(t, hub, "room-2", b)

	if err := hub.Route("room-1", websocket.Message{Type: websocket.TypeOffer, To: "b"}); err != websocket.ErrPeerNotFound {
		t.Errorf("Route() across rooms error = %v, want ErrPeerNotFound", err)
	}
	if len(b.messages()) != 0 {
		t.Errorf("b.messages() = %+v, want none (different room)", b.messages())
	}
}

func mustJoin(t *testing.T, hub *websocket.Hub, room string, p *fakePeer) []string {
	t.Helper()
	existing, err := hub.Join(room, p)
	if err != nil {
		t.Fatalf("Join(%q) error = %v", room, err)
	}
	return existing
}
