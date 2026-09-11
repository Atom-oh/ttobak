package repository

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/ttobak/backend/internal/model"
)

var (
	ErrInvalidMeetingFilter = errors.New("invalid meeting account filter")
	ErrInvalidMeetingCursor = errors.New("invalid meeting cursor")
)

// NormalizeMeetingAccountIDs accepts opaque URL-safe IDs, including existing
// UUIDs. Empty selection means all accounts; empty elements are malformed.
// The non-nil result distinguishes the multi-filter API from legacy callers.
func NormalizeMeetingAccountIDs(ids []string) ([]string, error) {
	unique := make(map[string]bool)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if len(id) == 0 || len(id) > 128 {
			return nil, ErrInvalidMeetingFilter
		}
		for i, c := range id {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
				i > 0 && (c == '-' || c == '_') {
				continue
			}
			return nil, ErrInvalidMeetingFilter
		}
		unique[id] = true
		if len(unique) > 100 {
			return nil, ErrInvalidMeetingFilter
		}
	}
	out := make([]string, 0, len(unique))
	for id := range unique {
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}

// decodeMeetingKey validates the string-only key shape before the general
// repository decoder can silently turn a malformed cursor into a first page.
func decodeMeetingKey(cursor string, size int) (map[string]string, error) {
	if len(cursor) > 8192 {
		return nil, ErrInvalidMeetingCursor
	}
	data, err := base64.StdEncoding.Strict().DecodeString(cursor)
	if err != nil {
		return nil, ErrInvalidMeetingCursor
	}
	var key map[string]string
	if err := json.Unmarshal(data, &key); err != nil || len(key) != size {
		return nil, ErrInvalidMeetingCursor
	}
	for _, v := range key {
		if v == "" || len(v) > 2048 || strings.ContainsAny(v, "\x00\r\n") {
			return nil, ErrInvalidMeetingCursor
		}
	}
	return key, nil
}

// ValidateMeetingListCursor checks the exact index/partition scope. GSI1 also
// contains membership rows, so sparse pages can legitimately stop on one.
func ValidateMeetingListCursor(cursor, userID, tab string) error {
	if cursor == "" {
		return nil
	}
	size := 4
	if tab == "shared" {
		size = 2
	}
	key, err := decodeMeetingKey(cursor, size)
	if err != nil {
		return err
	}
	userPK := model.PrefixUser + userID
	if tab == "shared" {
		if key["PK"] != userPK || !keyHasSuffix(key["SK"], model.PrefixShare) {
			return ErrInvalidMeetingCursor
		}
		return nil
	}
	if key["GSI1PK"] != userPK || key["GSI1SK"] == "" {
		return ErrInvalidMeetingCursor
	}
	owned := key["PK"] == userPK && keyHasSuffix(key["SK"], model.PrefixMeeting)
	membership := (keyHasSuffix(key["PK"], model.PrefixAccount) || keyHasSuffix(key["PK"], model.PrefixProject)) &&
		key["SK"] == model.PrefixMember+userID && key["GSI1SK"] == key["PK"]
	if !owned && !membership {
		return ErrInvalidMeetingCursor
	}
	return nil
}

// ValidateMeetingRefCursor prevents a mutable continuation from changing its
// account partition. Membership is separately rechecked by the service.
func ValidateMeetingRefCursor(cursor, accountID string) error {
	if cursor == "" {
		return nil
	}
	key, err := decodeMeetingKey(cursor, 2)
	if err != nil {
		return err
	}
	if key["PK"] != model.PrefixAccount+accountID || !keyHasSuffix(key["SK"], model.PrefixMeetingRef) {
		return ErrInvalidMeetingCursor
	}
	return nil
}

func keyHasSuffix(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) > len(prefix)
}
