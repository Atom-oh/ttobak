package service

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

func privateFileKeyOwner(key, category string) (string, error) {
	parts := strings.Split(key, "/")
	if len(key) > 1024 || !utf8.ValidString(key) || len(parts) < 3 || parts[0] != category || !readingID.MatchString(parts[1]) {
		return "", ErrForbidden
	}
	for _, part := range parts[2:] {
		// Preserve legitimate escaped names while refusing traversal at any
		// decoding layer used by signed media URLs.
		for depth := 0; ; depth++ {
			if part == "" || part == "." || part == ".." || !utf8.ValidString(part) ||
				strings.ContainsAny(part, "/\\") || strings.ContainsFunc(part, unicode.IsControl) {
				return "", ErrForbidden
			}
			decoded, err := url.PathUnescape(part)
			if err != nil || decoded == part {
				break
			}
			if depth == 8 {
				return "", ErrForbidden
			}
			part = decoded
		}
	}
	return parts[1], nil
}

// ValidMeetingAssetKey binds an original uploaded asset to its authorized meeting.
func ValidMeetingAssetKey(key, category, ownerID, meetingID string) bool {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || !readingID.MatchString(meetingID) || parts[2] != meetingID {
		return false
	}
	owner, err := privateFileKeyOwner(key, category)
	return err == nil && owner == ownerID
}
