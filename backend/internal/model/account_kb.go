package model

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"time"
)

// Account KB ref rows live in the account partition:
//
//	ACCOUNT#{accountId} / KBREF#{sha256(s3Key) hex}
//
// A ref is the canonical account-owned relation for a KB file uploaded with
// the account. It is written before the upload URL is returned, so a ref may
// name an object that was never uploaded or was later deleted; readers check
// the object before listing it. Removing a ref never deletes the object.
const PrefixAccountKBRef = "KBREF#"

// Account KB list kinds.
const (
	AccountKBKindUpload  = "upload"
	AccountKBKindMeeting = "meeting"
	AccountKBKindNews    = "news"
	AccountKBKindTech    = "tech"
)

// Account KB index state labels.
const (
	AccountKBIndexed   = "INDEXED"
	AccountKBPending   = "PENDING"
	AccountKBFailed    = "FAILED"
	AccountKBUntracked = "UNTRACKED"
)

var accountKBRefID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// AccountKBRefID returns the ref ID for an S3 key.
func AccountKBRefID(s3Key string) string {
	sum := sha256.Sum256([]byte(s3Key))
	return hex.EncodeToString(sum[:])
}

// ValidAccountKBRefID reports whether id is 64 lowercase hex characters.
func ValidAccountKBRefID(id string) bool { return accountKBRefID.MatchString(id) }

// AccountKBRef binds one uploaded KB object to an account.
type AccountKBRef struct {
	AccountID  string    `dynamodbav:"accountId" json:"-"`
	RefID      string    `dynamodbav:"refId" json:"-"`
	S3Key      string    `dynamodbav:"s3Key" json:"-"`
	FileName   string    `dynamodbav:"fileName" json:"-"`
	UploadedBy string    `dynamodbav:"uploadedBy" json:"-"`
	CreatedAt  time.Time `dynamodbav:"createdAt" json:"-"`
}

// AccountKBItem is one row of the account KB list. It never carries an S3
// key or bucket name.
type AccountKBItem struct {
	Kind        string `json:"kind"` // upload|meeting|news|tech
	ID          string `json:"id"`
	Title       string `json:"title"`
	Date        string `json:"date"`
	UploadedBy  string `json:"uploadedBy,omitempty"`
	IndexState  string `json:"indexState"` // INDEXED|PENDING|FAILED|UNTRACKED
	CanDownload bool   `json:"canDownload"`
}

// AccountKBPage is one page of the account KB list.
type AccountKBPage struct {
	Items      []AccountKBItem `json:"items"`
	NextCursor string          `json:"nextCursor,omitempty"`
	// Truncated reports that a read bound was hit, so items may be missing
	// (tech links beyond the bounded link read).
	Truncated bool `json:"truncated,omitempty"`
}
