// Package websocket implements WebRTC signaling for group calls: it
// routes offers, answers, and ICE candidates between the browsers in a
// room (a mesh — every browser connects directly to every other browser).
// It never touches the media stream itself — that's WebRTC, peer-to-peer,
// once signaling completes.
package websocket

import "encoding/json"

// Type identifies what kind of signaling message this is.
type Type string

const (
	// TypePeers is sent by the hub to a newly joined client, listing the
	// IDs of peers already in the room. The new client waits for each of
	// them to send it an offer.
	TypePeers Type = "peers"
	// TypeJoin is sent by the hub to every existing peer when a new one
	// joins; From carries the new peer's ID. Existing peers respond by
	// sending that peer an offer.
	TypeJoin Type = "join"
	// TypeOffer carries a browser's WebRTC session description offer,
	// addressed to one specific peer via To.
	TypeOffer Type = "offer"
	// TypeAnswer carries a browser's WebRTC session description answer,
	// addressed to one specific peer via To.
	TypeAnswer Type = "answer"
	// TypeICECandidate carries a single ICE candidate, addressed to one
	// specific peer via To.
	TypeICECandidate Type = "ice-candidate"
	// TypeLeave is sent by a browser to hang up, or by the hub to every
	// remaining peer when one disconnects; From carries who left.
	TypeLeave Type = "leave"
)

// Message is exchanged between a browser and the gateway over the
// signaling WebSocket. Payload is opaque to the gateway — it's whatever
// WebRTC object (SDP, ICE candidate) the browser sent, relayed as-is to
// the addressed peer.
type Message struct {
	Type Type `json:"type"`
	// From is the sending peer's ID. The gateway sets this itself on
	// relay — a client-supplied value is never trusted.
	From string `json:"from,omitempty"`
	// To is the target peer's ID. Required for offer, answer, and
	// ice-candidate; unused otherwise.
	To string `json:"to,omitempty"`
	// Peers lists the IDs already in the room. Only set on TypePeers.
	Peers   []string        `json:"peers,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}
