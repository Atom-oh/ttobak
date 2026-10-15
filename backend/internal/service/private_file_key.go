package service

import (
	"net/url"
	"strings"
)

func privateFileKeyOwner(key, category string) (string, error) {
	parts := strings.Split(key, "/")
	if len(key) > 1024 || len(parts) < 3 || parts[0] != category || !readingID.MatchString(parts[1]) {
		return "", ErrForbidden
	}
	for _, part := range parts[2:] {
		// Preserve legitimate escaped names while refusing traversal at any
		// decoding layer used by signed media URLs.
		for depth := 0; ; depth++ {
			if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "/\\\x00\r\n") {
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
