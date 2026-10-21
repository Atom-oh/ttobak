package model

import (
	"crypto/sha256"
	"encoding/hex"
)

// ConnectorNoteID isolates an idempotency key by both owner and client.
func ConnectorNoteID(user, client, request string) string {
	hash := sha256.Sum256([]byte(user + "\x00" + client + "\x00" + request))
	return hex.EncodeToString(hash[:16])
}

// ConnectorNoteReceipt confirms creation, not a current read of note content.
type ConnectorNoteReceipt struct {
	DocID    string `json:"docId"`
	Replayed bool   `json:"replayed"`
}
