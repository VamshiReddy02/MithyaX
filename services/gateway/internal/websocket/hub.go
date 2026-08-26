package websocket

import (
	"errors"
	"sync"
)

// maxClientsPerRoom caps each room at a single 1:1 call.
const maxClientsPerRoom = 2

// ErrRoomFull is returned by Hub.Join when a room already has two peers.
var ErrRoomFull = errors.New("room is full")

// peer is anything the hub can register into a room and relay messages
// to. Client implements it; tests can supply a fake without a real
// network connection.
type peer interface {
	Send(Message)
}

// Hub manages every active call: it registers and removes peers, and
// forwards signaling messages between the (at most two) peers sharing a
// room.
type Hub struct {
	mu    sync.Mutex
	rooms map[string]map[peer]struct{}
}

// NewHub builds an empty Hub.
func NewHub() *Hub {
	return &Hub{rooms: make(map[string]map[peer]struct{})}
}

// Join registers p into room. If another peer is already waiting there,
// that peer receives a TypeJoin message. Returns ErrRoomFull if the room
// already has two peers.
func (h *Hub) Join(room string, p peer) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	clients := h.rooms[room]
	if len(clients) >= maxClientsPerRoom {
		return ErrRoomFull
	}
	if clients == nil {
		clients = make(map[peer]struct{})
		h.rooms[room] = clients
	}

	h.broadcastLocked(room, p, Message{Type: TypeJoin})
	clients[p] = struct{}{}
	return nil
}

// Leave removes p from room. If another peer remains, it receives a
// TypeLeave message. A now-empty room is discarded.
func (h *Hub) Leave(room string, p peer) {
	h.mu.Lock()
	defer h.mu.Unlock()

	clients, ok := h.rooms[room]
	if !ok {
		return
	}
	if _, ok := clients[p]; !ok {
		return
	}
	delete(clients, p)

	if len(clients) == 0 {
		delete(h.rooms, room)
		return
	}
	h.broadcastLocked(room, p, Message{Type: TypeLeave})
}

// Broadcast relays msg to every peer in room except sender.
func (h *Hub) Broadcast(room string, sender peer, msg Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.broadcastLocked(room, sender, msg)
}

func (h *Hub) broadcastLocked(room string, sender peer, msg Message) {
	for p := range h.rooms[room] {
		if p == sender {
			continue
		}
		p.Send(msg)
	}
}
