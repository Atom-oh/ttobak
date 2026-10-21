package service

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"
)

// A complete source group's JSON is reconstructed before callers consume its
// records. The callback owns continuation; this helper never invents a source
// position from the byte budget.
func renderCollectionReading(kind, phase, text, binding, group string, offset, size int, extras readingObject,
	advance func(bool, int) (string, bool, error)) ([]byte, error) {
	innerBinding := readingHash([]string{binding, group})
	inner := ReadingOptions{PageSize: size, MaxBytes: 10000}
	if offset > 0 {
		raw, _ := json.Marshal(readingCursor{1, innerBinding, offset})
		inner.Cursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	revision, _ := base64.RawURLEncoding.DecodeString(group)
	var renderErr error
	body, err := paginateReading(text, []readingWindow{{end: utf8.RuneCountInString(text)}}, inner, innerBinding, func(chunks []readingChunk, fragment readingObject) readingObject {
		part := ""
		for _, chunk := range chunks {
			part += chunk.Text
		}
		cursor, complete, err := advance(fragment["complete"].(bool), fragment["endOffset"].(int))
		renderErr = err
		var continuation interface{}
		if !complete {
			continuation = cursor
		}
		delete(fragment, "nextCursor")
		value := readingObject{"kind": kind, "phase": phase, "consistency": "current_group", "revision": hex.EncodeToString(revision), "dataJson": part, "fragment": fragment, "page": readingObject{"complete": complete, "nextCursor": continuation}}
		for key, item := range extras {
			value[key] = item
		}
		return value
	})
	if err != nil {
		return nil, err
	}
	if renderErr != nil {
		return nil, renderErr
	}
	return body, nil
}
