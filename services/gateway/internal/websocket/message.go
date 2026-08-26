// Package websocket implements WebRTC signaling: it relays offers, answers,
// and ICE candidates between the two browsers in a call. It never touches
// the media stream itself — that's WebRTC, peer-to-peer, once signaling
// completes.
package websocket

import "encoding/json"

// Type identifies what kind of signaling message this is.
type Type string

const (
	// TypeJoin is sent by the hub to a peer already in the room when
	// another peer joins it.
	TypeJoin Type = "join"
	// TypeOffer carries a browser's WebRTC session description offer.
	TypeOffer Type = "offer"
	// TypeAnswer carries a browser's WebRTC session description answer.
	TypeAnswer Type = "answer"
	// TypeICECandidate carries a single ICE candidate.
	TypeICECandidate Type = "ice-candidate"
	// TypeLeave is sent by a browser to hang up, or by the hub to the
	// remaining peer when the other one disconnects.
	TypeLeave Type = "leave"
)

// Message is exchanged between a browser and the gateway over the
// signaling WebSocket. Payload is opaque to the gateway — it's whatever
// WebRTC object (SDP, ICE candidate) the browser sent, relayed as-is to
// the other peer in the room.
type Message struct {
	Type    Type            `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}
