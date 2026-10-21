package model

import (
	"encoding/hex"

	"github.com/google/uuid"
)

// ValidConnectorNoteReceiptID accepts existing legacy receipt identities and
// canonical UUIDs. Receipt identity is server state, never caller input.
func ValidConnectorNoteReceiptID(id string) bool {
	if len(id) == 32 {
		decoded, err := hex.DecodeString(id)
		return err == nil && hex.EncodeToString(decoded) == id
	}
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}

// ConnectorNoteReceipt confirms creation, not a current read of note content.
type ConnectorNoteReceipt struct {
	DocID    string `json:"docId"`
	Replayed bool   `json:"replayed"`
}
