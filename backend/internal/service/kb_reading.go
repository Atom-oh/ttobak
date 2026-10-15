package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Compact field names keep even a 1024-byte S3 key within the 2048-character
// cursor limit. Keys are base64 inside JSON so control characters cannot expand
// the cursor sixfold. This is a continuation, never an authorization token.
type kbReadingCursor struct {
	Version  int    `json:"v"`
	Binding  string `json:"b"`
	After    string `json:"k"`
	Revision string `json:"r"`
}

func kbObjectRevision(key string, etag *string, size *int64, modified *time.Time) string {
	return readingHash([]interface{}{key, aws.ToString(etag), aws.ToInt64(size),
		aws.ToTime(modified).UTC().Format(time.RFC3339Nano)})
}

func (s *KBService) currentKBMetadata(ctx context.Context, key string) (*s3.HeadObjectOutput, error) {
	result, err := s.s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.kbBucketName), Key: aws.String(key),
	})
	if err != nil {
		var missing *s3types.NotFound
		if errors.As(err, &missing) {
			return nil, ErrStaleReadingCursor
		}
		return nil, err
	}
	if result.ETag == nil || result.ContentLength == nil || result.LastModified == nil {
		return nil, ErrReadingData
	}
	return result, nil
}

// ReadFiles returns one bounded, current-user listing page. General-purpose S3
// buckets order keys lexicographically; StartAfter lets the server resume at the
// last consumed object if the encoded result budget fills before MaxKeys.
func (s *KBService) ReadFiles(ctx context.Context, userID string, q url.Values) ([]byte, error) {
	if !readingID.MatchString(userID) {
		return nil, ErrForbidden
	}
	for key := range q {
		if key != "pageSize" && key != "cursor" {
			return nil, ErrInvalidReadingOptions
		}
	}
	o, err := parseDocumentCatalog(q)
	if err != nil {
		return nil, err
	}
	prefix := "kb/" + userID + "/"
	binding := readingHash([]string{"kb-reading-v1", s.kbBucketName, userID})
	c := kbReadingCursor{Version: 1, Binding: binding}
	after := ""
	if o.cursor != "" {
		raw, err := base64.RawURLEncoding.Strict().DecodeString(o.cursor)
		if err != nil || json.Unmarshal(raw, &c) != nil {
			return nil, ErrInvalidReadingCursor
		}
		canonical, _ := json.Marshal(c)
		if !bytes.Equal(raw, canonical) || c.Version != 1 {
			return nil, ErrInvalidReadingCursor
		}
		if c.Binding != binding {
			return nil, ErrStaleReadingCursor
		}
		key, err := base64.RawURLEncoding.Strict().DecodeString(c.After)
		if err != nil || !utf8.Valid(key) || !strings.HasPrefix(string(key), prefix) || len(key) > 1024 {
			return nil, ErrInvalidReadingCursor
		}
		after = string(key)
		if after != prefix {
			owner, err := privateFileKeyOwner(after, "kb")
			if err != nil || owner != userID {
				return nil, ErrInvalidReadingCursor
			}
			current, err := s.currentKBMetadata(ctx, after)
			if err != nil {
				return nil, err
			}
			if kbObjectRevision(after, current.ETag, current.ContentLength, current.LastModified) != c.Revision {
				return nil, ErrStaleReadingCursor
			}
		} else if c.Revision != "" {
			return nil, ErrInvalidReadingCursor
		}
	}
	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(s.kbBucketName), Prefix: aws.String(prefix), MaxKeys: aws.Int32(int32(o.pageSize)),
	}
	if after != "" {
		input.StartAfter = aws.String(after)
	}
	listed, err := s.s3Client.ListObjectsV2(ctx, input)
	if err != nil {
		return nil, err
	}
	if len(listed.Contents) > o.pageSize {
		return nil, ErrReadingData
	}
	files := []readingObject{}
	dependencies := map[string]string{}
	render := func(more bool) ([]byte, error) {
		var cursor interface{}
		if more {
			raw, _ := json.Marshal(c)
			encoded := base64.RawURLEncoding.EncodeToString(raw)
			if len(encoded) > 2048 {
				return nil, ErrReadingData
			}
			cursor = encoded
		}
		return readingJSON(readingObject{"files": files, "consistency": "current_per_item",
			"page": readingObject{"unit": "items", "returned": len(files), "complete": !more, "nextCursor": cursor}})
	}
	consumed := 0
	last := after
	for _, object := range listed.Contents {
		key := aws.ToString(object.Key)
		if !utf8.ValidString(key) || !strings.HasPrefix(key, prefix) || len(key) > 1024 || key <= last {
			return nil, ErrReadingData
		}
		if key == prefix {
			c.After, c.Revision, last = base64.RawURLEncoding.EncodeToString([]byte(key)), "", key
			consumed++
			continue
		}
		owner, err := privateFileKeyOwner(key, "kb")
		if err != nil || owner != userID {
			return nil, ErrReadingData
		}
		current, err := s.currentKBMetadata(ctx, key)
		if err != nil {
			return nil, err
		}
		revision := kbObjectRevision(key, current.ETag, current.ContentLength, current.LastModified)
		name := strings.TrimPrefix(key, prefix)
		mime := "application/octet-stream"
		switch strings.ToLower(filepath.Ext(name)) {
		case ".pdf":
			mime = "application/pdf"
		case ".md":
			mime = "text/markdown"
		case ".ppt", ".pptx":
			mime = "application/vnd.ms-powerpoint"
		case ".doc", ".docx":
			mime = "application/msword"
		}
		truncated := []string{}
		file := readingObject{
			"fileId": kbFileID(key), "fileName": readingLimit(name, 128, "fileName", &truncated),
			"fileType": mime, "size": aws.ToInt64(current.ContentLength),
			"lastModified":     current.LastModified.UTC().Format(time.RFC3339),
			"metadataRevision": revision, "metadataTruncated": truncated,
		}
		before := c
		c.After, c.Revision = base64.RawURLEncoding.EncodeToString([]byte(key)), revision
		files = append(files, file)
		body, err := render(true)
		if err != nil {
			return nil, err
		}
		if len(body) > 10000 {
			files, c = files[:len(files)-1], before
			break
		}
		dependencies[key], last = revision, key
		consumed++
	}
	more := consumed < len(listed.Contents) || aws.ToBool(listed.IsTruncated)
	if more && consumed == 0 {
		return nil, ErrReadingData
	}
	for key, revision := range dependencies {
		current, err := s.currentKBMetadata(ctx, key)
		if err != nil {
			return nil, err
		}
		if kbObjectRevision(key, current.ETag, current.ContentLength, current.LastModified) != revision {
			return nil, ErrStaleReadingCursor
		}
	}
	body, err := render(more)
	if err != nil || len(body) > 10000 {
		return nil, ErrReadingData
	}
	return body, nil
}
