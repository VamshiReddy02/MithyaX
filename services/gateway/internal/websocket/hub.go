package websocket

import (
	"errors"
	"sync"
)

// maxClientsPerRoom bounds a room to a size where mesh calling (every
// browser connects directly to every other browser) is still reasonable —
// each participant uploads its own video once per other participant, so
// cost grows with the square of room size.
const maxClientsPerRoom = 6

// ErrRoomFull is returned by Hub.Join when a room is already at capacity.
var ErrRoomFull = errors.New("room is full")

// ErrPeerNotFound is returned by Hub.Route when the message's To peer
// isn't (or is no longer) in the room.
var ErrPeerNotFound = errors.New("peer not found")

// peer is anything the hub can register into a room, address by ID, and
// relay messages to. Client implements it; tests can supply a fake
// without a real network connection.
type peer interface {
	ID() string
	Send(Message)
}

// Hub manages every active call: it registers and removes peers, and
// routes signaling messages between the peers sharing a room.
type Hub struct {
	mu    sync.Mutex
	rooms map[string]map[string]peer // room -> peer ID -> peer
}

// NewHub builds an empty Hub.
func NewHub() *Hub {
	return &Hub{rooms: make(map[string]map[string]peer)}
}

// Join registers p into room and returns the IDs of peers already there.
// Each of those peers receives a TypeJoin message naming p, so it can
// initiate a connection to it. Returns ErrRoomFull if the room is at
// capacity.
func (h *Hub) Join(room string, p peer) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	clients := h.rooms[room]
	if len(clients) >= maxClientsPerRoom {
		return nil, ErrRoomFull
	}
	if clients == nil {
		clients = make(map[string]peer)
		h.rooms[room] = clients
	}

	existing := make([]string, 0, len(clients))
	for id := range clients {
		existing = append(existing, id)
	}

	h.broadcastLocked(clients, p.ID(), Message{Type: TypeJoin, From: p.ID()})
	clients[p.ID()] = p
	return existing, nil
}

// Leave removes p from room. Every remaining peer receives a TypeLeave
// message naming p, so it can tear down its connection to it. A now-empty
// room is discarded.
func (h *Hub) Leave(room string, p peer) {
	h.mu.Lock()
	defer h.mu.Unlock()

	clients, ok := h.rooms[room]
	if !ok {
		return
	}
	if _, ok := clients[p.ID()]; !ok {
		return
	}
	delete(clients, p.ID())

	if len(clients) == 0 {
		delete(h.rooms, room)
		return
	}
	h.broadcastLocked(clients, p.ID(), Message{Type: TypeLeave, From: p.ID()})
}

// Route delivers msg to the single peer named by msg.To in room. Returns
// ErrPeerNotFound if that peer isn't in the room (e.g. it just left).
func (h *Hub) Route(room string, msg Message) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	clients, ok := h.rooms[room]
	if !ok {
		return ErrPeerNotFound
	}
	target, ok := clients[msg.To]
	if !ok {
		return ErrPeerNotFound
	}
	target.Send(msg)
	return nil
}

func (h *Hub) broadcastLocked(clients map[string]peer, exceptID string, msg Message) {
	for id, p := range clients {
		if id == exceptID {
			continue
		}
		p.Send(msg)
	}
}
