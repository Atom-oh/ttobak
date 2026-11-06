package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// audioBatchWire is one meeting row evaluating the batch conditions.
type audioBatchWire struct {
	t             *testing.T
	keys          []string // nil: audioKeys absent
	status, batch string   // batch "": audioUploadBatchBy absent
}

func (w *audioBatchWire) item() string {
	keys, _ := json.Marshal(w.keys)
	item := `"status":{"S":"` + w.status + `"},"audioKeys":{"L":` + regexp.MustCompile(`"[^"]*"`).ReplaceAllString(string(keys), `{"S":$0}`) + `}`
	if w.batch != "" {
		item += `,"audioUploadBatchBy":{"S":"` + w.batch + `"}`
	}
	return `{"PK":{"S":"USER#owner"},` + item + `}`
}

func (w *audioBatchWire) repo() *DynamoDBRepository {
	return summarySDKRepo(func(req *http.Request) (*http.Response, error) {
		var in struct {
			notesRevisionRequest
			ReturnValuesOnConditionCheckFailure string
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			w.t.Fatal(err)
		}
		if strings.HasSuffix(req.Header.Get("X-Amz-Target"), "GetItem") {
			return summaryHTTPResponse(200, `{"Item":`+w.item()+`}`, nil), nil
		}
		ok := w.keys == nil || w.status == "done"
		slot := regexp.MustCompile(`audioKeys\[(\d+)\]`).FindStringSubmatch(in.UpdateExpression)
		if slot != nil {
			i, _ := strconv.Atoi(slot[1])
			ok = w.keys != nil && i < len(w.keys) && (w.status == "recording" || w.status == "transcribing")
			if strings.Contains(in.ConditionExpression, "audioUploadBatchBy") {
				ok = ok && w.batch == in.ExpressionAttributeValues[":batchBy"]["S"]
			}
		}
		if !ok {
			item := ""
			if in.ReturnValuesOnConditionCheckFailure == "ALL_OLD" {
				item = `,"Item":` + w.item()
			}
			return summaryHTTPResponse(400, `{"__type":"ConditionalCheckFailedException"`+item+`}`, nil), nil
		}
		if slot != nil {
			i, _ := strconv.Atoi(slot[1])
			w.keys[i], w.status = in.ExpressionAttributeValues[":key"]["S"].(string), "transcribing"
		} else {
			w.keys, w.status = []string{"", ""}, "transcribing"
			w.batch, _ = notesWireValue(in.notesRevisionRequest, in.UpdateExpression, "audioUploadBatchBy")
		}
		return summaryHTTPResponse(200, `{}`, nil), nil
	})
}

func (w *audioBatchWire) upload(uploader, key string, part int) error {
	if err := w.repo().PreAllocateAudioKeys(context.Background(), "owner", "m", uploader, 2); err != nil {
		return err
	}
	return w.repo().SetAudioKeyAtIndex(context.Background(), "owner", "m", uploader, key, part)
}

func TestAudioBatchOwnership(t *testing.T) {
	// Racing members: one owner; the other is refused, no slot overwritten.
	w := &audioBatchWire{t: t, status: "done", keys: []string{"old"}}
	if err := w.upload("alice", "a0", 0); err != nil {
		t.Fatal(err)
	}
	if err := w.upload("bob", "b0", 0); !errors.Is(err, ErrAudioBatchInProgress) || w.batch != "alice" || w.keys[0] != "a0" {
		t.Fatal(err, w.batch, w.keys)
	}
	if err := w.upload("alice", "a1", 1); err != nil || w.keys[1] != "a1" {
		t.Fatal(err, w.keys)
	}
	// The slot write itself checks the owner.
	for _, batch := range []string{"alice", ""} {
		w := &audioBatchWire{t: t, keys: []string{"x0", ""}, status: "transcribing", batch: batch}
		err := w.repo().SetAudioKeyAtIndex(context.Background(), "owner", "m", "bob", "b0", 0)
		if !errors.Is(err, ErrAudioBatchInProgress) || w.keys[0] != "x0" {
			t.Fatal(batch, err, w.keys)
		}
	}
	// A stale member marker never blocks the creator or pipeline.
	for _, uploader := range []string{"owner", ""} {
		w := &audioBatchWire{t: t, keys: []string{"", ""}, status: "transcribing", batch: "alice"}
		if err := w.upload(uploader, "o1", 1); err != nil || w.keys[1] != "o1" {
			t.Fatal(uploader, err, w.keys)
		}
	}
}
