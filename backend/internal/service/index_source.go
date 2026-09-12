package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type IndexSnapshot struct {
	Record    *model.IndexRecord
	Revision  string
	Outcome   string
	ErrorCode string
	Objects   []IndexObject
	Parts     []IndexPart
}

// IndexSourceRevision hashes UTF-8 length-prefixed strings (N:value), avoiding
// cross-language JSON escaping differences. See the backend plan for the order.
func IndexSourceRevision(key model.IndexResource, fields map[string]interface{}, objects []IndexObject, outcome string) (string, error) {
	hash := sha256.New()
	add := func(value string) {
		fmt.Fprintf(hash, "%d:", len([]byte(value)))
		hash.Write([]byte(value))
	}
	for _, value := range []string{"canonical-v1", key.PK, key.SK, outcome} {
		add(value)
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		add("field")
		add(name)
		if fields[name] == nil {
			add("null")
			add("")
			continue
		}
		value, ok := fields[name].(string)
		if !ok || !utf8.ValidString(value) {
			return "", ErrIndexInvalid
		}
		add("string")
		add(value)
	}
	pins := append([]IndexObject(nil), objects...)
	sort.Slice(pins, func(i, j int) bool { return pins[i].Key < pins[j].Key })
	for _, object := range pins {
		for _, value := range []string{"object", object.Key, object.ETag, object.VersionID,
			strconv.FormatInt(object.Size, 10), strconv.FormatBool(object.Missing),
			object.Metadata["source-etag"], object.Metadata["source-version-id"]} {
			add(value)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func indexString(record *model.IndexRecord, name string) (string, error) {
	value, exists := record.Fields[name]
	if !exists || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok || !utf8.ValidString(text) {
		return "", fmt.Errorf("%w: invalid %s", ErrIndexInvalid, name)
	}
	return text, nil
}

// ReadSource produces a revision even for missing files/previews. Reconciliation
// can therefore detect bytes appearing/changing without a DynamoDB stream event.
func (s *IndexingService) ReadSource(ctx context.Context, key model.IndexResource, bodies bool) (*IndexSnapshot, error) {
	canonical, ok := model.CanonicalIndexResource(key.PK, key.SK)
	if !ok || canonical != key {
		return nil, ErrIndexInvalid
	}
	record, err := s.repo.GetIndexSource(ctx, key)
	if err != nil {
		return nil, err
	}
	snapshot := &IndexSnapshot{Record: record, Outcome: model.IndexIndexed, Objects: []IndexObject{}}
	if record == nil {
		snapshot.Outcome = model.IndexDeleted
		snapshot.Revision, err = IndexSourceRevision(key, nil, nil, snapshot.Outcome)
		return snapshot, err
	}
	get := func(name string) string {
		text, e := indexString(record, name)
		if e != nil {
			err = e
		}
		return text
	}
	idField := "docId"
	if key.Kind == "meeting" {
		idField = "meetingId"
	}
	if get(idField) != key.ID {
		return nil, fmt.Errorf("%w: canonical identity mismatch", ErrIndexInvalid)
	}
	title, content := get("title"), get("content")
	if err != nil {
		return nil, err
	}
	head := func(objectKey string) (IndexObject, error) {
		object, e := s.objects.Head(ctx, objectKey)
		if errors.Is(e, ErrIndexMissing) {
			object, e = IndexObject{Key: objectKey, Missing: true}, nil
		}
		if e != nil {
			return object, e
		}
		// Only converter source-binding metadata participates in the revision.
		meta := map[string]string{}
		for _, name := range []string{"source-etag", "source-version-id"} {
			if value := object.Metadata[name]; value != "" {
				meta[name] = value
			}
		}
		object.Metadata = meta
		snapshot.Objects = append(snapshot.Objects, object)
		return object, nil
	}
	if key.Kind == "meeting" {
		_, owner, _ := strings.Cut(key.PK, "#")
		if get("userId") != owner {
			return nil, ErrIndexInvalid
		}
		m := &model.Meeting{TranscriptA: get("transcriptA"), TranscriptB: get("transcriptB"), SelectedTranscript: get("selectedTranscript")}
		notes, actions := get("notes"), get("actionItems")
		if err != nil {
			return nil, err
		}
		if actions != "" && !json.Valid([]byte(actions)) {
			return nil, ErrIndexInvalid
		}
		objects := map[string]IndexObject{}
		// Bind both variants' external bytes so a hydrated empty selected
		// variant can fall back without changing the revision algorithm.
		for _, field := range []string{"transcriptA", "transcriptB"} {
			ref := get(field)
			if !strings.HasPrefix(ref, "s3://") {
				continue
			}
			objectKey, e := repository.IndexTranscriptKey(s.assetsBucket, key.ID, field, ref)
			if e != nil {
				return nil, e
			}
			object, e := head(objectKey)
			if e != nil {
				return nil, e
			}
			objects[field] = object
		}
		if bodies {
			for attempt := 0; attempt < 2; attempt++ {
				text, variant := selectMeetingTranscript(m)
				field := "transcript" + variant
				if variant == "" {
					break
				}
				if object, exists := objects[field]; exists {
					if object.Missing {
						return nil, ErrIndexMissing
					}
					bytes, e := s.objects.Read(ctx, object)
					if e != nil {
						return nil, e
					}
					if !utf8.Valid(bytes) {
						return nil, ErrIndexInvalid
					}
					text = string(bytes)
					if variant == "A" {
						m.TranscriptA = text
					} else {
						m.TranscriptB = text
					}
					delete(objects, field)
					if strings.TrimSpace(text) == "" {
						continue
					}
				}
				break
			}
			transcript, variant := selectMeetingTranscript(m)
			if strings.HasPrefix(transcript, "s3://") {
				return nil, ErrIndexInvalid
			}
			body := "# " + title + "\n\n## User notes\n\n" + notes + "\n\n## Saved summary\n\n" + content +
				"\n\n## Action items (saved JSON)\n\n" + actions + "\n\n## Selected transcript " + variant + "\n\n" + transcript + "\n"
			snapshot.Parts = []IndexPart{{Name: "meeting.md", ContentType: "text/markdown; charset=utf-8", Body: []byte(body)}}
		}
	} else {
		fileKey := get("fileKey")
		if err != nil {
			return nil, err
		}
		if content != "" && bodies {
			snapshot.Parts = append(snapshot.Parts, IndexPart{Name: "document.md", ContentType: "text/markdown; charset=utf-8", Body: []byte("# " + title + "\n\n" + content)})
		}
		if fileKey != "" {
			components := strings.Split(fileKey, "/")
			if len(components) < 3 || components[0] != "docs" {
				return nil, ErrIndexInvalid
			}
			if _, ok := model.CanonicalIndexResource("USER#"+components[1], "DOC#"+key.ID); !ok {
				return nil, ErrIndexInvalid
			}
			for _, component := range components {
				if component == "" || component == "." || component == ".." {
					return nil, ErrIndexInvalid
				}
			}
			if key.Kind == "personalDocument" && key.PK != "USER#"+components[1] {
				return nil, ErrIndexInvalid
			}
			original, e := head(fileKey)
			if e != nil {
				return nil, e
			}
			object, extension := original, strings.ToLower(path.Ext(fileKey))
			if original.Missing {
				snapshot.Outcome, snapshot.ErrorCode = model.IndexWaitingSource, "FILE_MISSING"
			}
			if extension == ".ppt" || extension == ".pptx" {
				preview, e := head(SidecarPDFKey(fileKey))
				if e != nil {
					return nil, e
				}
				if original.Missing || preview.Missing || preview.Metadata["source-etag"] == "" ||
					strings.Trim(preview.Metadata["source-etag"], "\"") != strings.Trim(original.ETag, "\"") ||
					(original.VersionID != "" && preview.Metadata["source-version-id"] != original.VersionID) {
					snapshot.Outcome, snapshot.ErrorCode = model.IndexWaitingSource, "PREVIEW_PENDING"
				}
				object, extension = preview, ".pdf"
			}
			contentTypes := map[string]string{".pdf": "application/pdf", ".txt": "text/plain", ".md": "text/markdown",
				".html": "text/html", ".doc": "application/msword", ".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
				".csv": "text/csv", ".xls": "application/vnd.ms-excel", ".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}
			mime, supported := contentTypes[extension]
			if !supported {
				snapshot.Outcome, snapshot.ErrorCode = model.IndexWaitingSource, "UNSUPPORTED_FILE"
			}
			if supported && snapshot.Outcome == model.IndexIndexed && object.Size == 0 {
				snapshot.Outcome, snapshot.ErrorCode = model.IndexWaitingSource, "EMPTY_FILE"
			}
			if supported && snapshot.Outcome == model.IndexIndexed && bodies {
				bytes, e := s.objects.Read(ctx, object)
				if e != nil {
					return nil, e
				}
				if strings.HasPrefix(mime, "text/") && !utf8.Valid(bytes) {
					return nil, ErrIndexInvalid
				}
				snapshot.Parts = append(snapshot.Parts, IndexPart{Name: "file" + extension, ContentType: mime, Body: bytes})
			}
		} else if strings.TrimSpace(content) == "" {
			snapshot.Outcome, snapshot.ErrorCode = model.IndexWaitingSource, "EMPTY_DOCUMENT"
		}
	}
	if err != nil {
		return nil, err
	}
	for _, part := range snapshot.Parts {
		if int64(len(part.Body)) > MaxIndexObjectBytes {
			return nil, ErrIndexInvalid
		}
	}
	if snapshot.Outcome != model.IndexIndexed {
		snapshot.Parts = nil
	}
	snapshot.Revision, err = IndexSourceRevision(key, record.Fields, snapshot.Objects, snapshot.Outcome)
	if err == nil && snapshot.Outcome == model.IndexIndexed {
		// Validate before publication (also on HEAD-only freshness checks), so
		// an unsupported sidecar cannot masquerade as an indexable revision.
		_, err = indexMetadata(key, snapshot, "00000000-0000-0000-0000-000000000000")
	}
	return snapshot, err
}

func indexMetadata(key model.IndexResource, snapshot *IndexSnapshot, run string) ([]byte, error) {
	bindings, err := json.Marshal(snapshot.Objects)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(map[string]interface{}{"metadataAttributes": map[string]interface{}{
		"resourceKind": key.Kind, "sourcePK": key.PK, "sourceSK": key.SK,
		"resourceId": key.ID, "sourceRevision": snapshot.Revision, "indexRunId": run,
		"sourceObjects": string(bindings), "indexSchema": "canonical-v1",
	}})
	if len(data) > 10000 {
		return nil, fmt.Errorf("%w: metadata exceeds provider size limit", ErrIndexInvalid)
	}
	return data, err
}

func legacyIndexKeys(key model.IndexResource) []string {
	if key.Kind != "meeting" {
		return nil
	}
	_, owner, _ := strings.Cut(key.PK, "#")
	base := "meetings/" + owner + "/" + key.ID + ".md"
	return []string{base, base + ".metadata.json"}
}
