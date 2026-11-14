package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

const (
	maxServicesInUse         = 15
	maxServiceNameRunes      = 120
	maxServicesInUseBytes    = 64 * 1024
	servicesInUseMaxAge      = 45 * 24 * time.Hour
	servicesInUseMaxSkew     = 24 * time.Hour
	servicesInUseVersion     = 1
	servicesInUseKeyTemplate = "accounts/%s/derived/services-in-use.json"
)

// customerUpdateAccountIDPattern is run-v1.json's accountIdPattern.
var customerUpdateAccountIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ErrServicesUnavailable means the services object read was denied (HTTP 403
// AccessDenied). Without s3:ListBucket that is S3's answer for a missing key,
// so callers count it without logging.
var ErrServicesUnavailable = errors.New("services in use unavailable")

// ServicesInUseReader returns the AWS service names an account uses.
type ServicesInUseReader interface {
	ServicesInUse(ctx context.Context, accountID string) ([]string, error)
}

type servicesInUseS3 interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// S3ServicesInUseReader reads exactly one billing object per account, the
// names-only accounts/{accountId}/derived/services-in-use.json (ADR-012). It
// reads no other billing object and never calls the Billing BFF.
type S3ServicesInUseReader struct {
	client servicesInUseS3
	bucket string
	now    func() time.Time
}

// NewS3ServicesInUseReader creates the reader; an empty bucket disables it.
func NewS3ServicesInUseReader(client servicesInUseS3, bucket string) *S3ServicesInUseReader {
	return &S3ServicesInUseReader{client: client, bucket: bucket, now: time.Now}
}

type servicesInUseObject struct {
	Version     int               `json:"version"`
	GeneratedAt string            `json:"generatedAt"`
	Services    []json.RawMessage `json:"services"`
}

// ServicesInUse returns at most 15 service names. A missing, unset, oversized,
// malformed, wrong-version or stale (generatedAt unparseable, over 45 days
// old, or more than 24 hours in the future) object is "no service signal":
// nil, nil. A denied read is ErrServicesUnavailable. An invalid account id is
// ErrInvalidInput and never reaches S3; other S3 errors are returned.
func (r *S3ServicesInUseReader) ServicesInUse(ctx context.Context, accountID string) ([]string, error) {
	if !customerUpdateAccountIDPattern.MatchString(accountID) {
		return nil, ErrInvalidInput
	}
	if r == nil || r.client == nil || r.bucket == "" {
		return nil, nil
	}
	out, err := r.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(fmt.Sprintf(servicesInUseKeyTemplate, accountID)),
	})
	if s3Missing(err) {
		return nil, nil
	}
	if s3AccessDenied(err) {
		return nil, ErrServicesUnavailable
	}
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	body, err := io.ReadAll(io.LimitReader(out.Body, maxServicesInUseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxServicesInUseBytes {
		return nil, nil
	}
	var obj servicesInUseObject
	if json.Unmarshal(body, &obj) != nil || obj.Version != servicesInUseVersion {
		return nil, nil
	}
	generatedAt, err := time.Parse(time.RFC3339, obj.GeneratedAt)
	age := r.now().Sub(generatedAt)
	if err != nil || age > servicesInUseMaxAge || age < -servicesInUseMaxSkew {
		return nil, nil
	}
	names := make([]string, 0, maxServicesInUse)
	for _, raw := range obj.Services {
		var name string
		if json.Unmarshal(raw, &name) != nil {
			continue
		}
		name = collapseSpace(name)
		if name == "" || utf8.RuneCountInString(name) > maxServiceNameRunes {
			continue
		}
		names = append(names, name)
		if len(names) == maxServicesInUse {
			break
		}
	}
	return names, nil
}

// s3AccessDenied reports an S3 403 or AccessDenied error.
func s3AccessDenied(err error) bool {
	var resp *awshttp.ResponseError
	if errors.As(err, &resp) && resp.HTTPStatusCode() == 403 {
		return true
	}
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == "AccessDenied"
}

// collapseSpace trims s and collapses every whitespace run to one space, so
// customer text cannot carry newlines or tabs into a prompt.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
