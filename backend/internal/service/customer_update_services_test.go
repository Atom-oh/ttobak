package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type fakeServicesS3 struct {
	body  string
	err   error
	keys  []string
	bucks []string
}

func (f *fakeServicesS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.keys = append(f.keys, aws.ToString(in.Key))
	f.bucks = append(f.bucks, aws.ToString(in.Bucket))
	if f.err != nil {
		return nil, f.err
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader([]byte(f.body)))}, nil
}

var servicesTestNow = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

func servicesBody(version int, generatedAt string, services string) string {
	return `{"version":` + strconv.Itoa(version) + `,"source":"COT","versionId":"v1","generatedAt":"` + generatedAt + `","services":` + services + `}`
}

func newTestServicesReader(f *fakeServicesS3, bucket string) *S3ServicesInUseReader {
	r := NewS3ServicesInUseReader(f, bucket)
	r.now = func() time.Time { return servicesTestNow }
	return r
}

func TestServicesInUseReader(t *testing.T) {
	fresh := servicesTestNow.Add(-44 * 24 * time.Hour).Format(time.RFC3339)
	stale := servicesTestNow.Add(-46 * 24 * time.Hour).Format(time.RFC3339)
	ahead := servicesTestNow.Add(23 * time.Hour).Format(time.RFC3339)
	future := servicesTestNow.Add(25 * time.Hour).Format(time.RFC3339)
	long := strings.Repeat("가", 121)
	many := `[` + strings.TrimSuffix(strings.Repeat(`"S",`, 20), ",") + `]`
	cases := []struct {
		name   string
		body   string
		err    error
		want   []string
		wantE  bool
		bucket string
	}{
		{name: "fresh object", body: servicesBody(1, fresh, `["Amazon Bedrock","Amazon S3"]`), want: []string{"Amazon Bedrock", "Amazon S3"}},
		{name: "unset bucket", body: servicesBody(1, fresh, `["Amazon S3"]`), bucket: "-"},
		{name: "missing object", err: &s3types.NoSuchKey{}},
		{name: "other s3 error", err: errors.New("throttled"), wantE: true},
		{name: "wrong version", body: servicesBody(2, fresh, `["Amazon S3"]`)},
		{name: "invalid json", body: `{"version":1,`},
		{name: "stale 46 days", body: servicesBody(1, stale, `["Amazon S3"]`)},
		{name: "slightly ahead clock", body: servicesBody(1, ahead, `["Amazon S3"]`), want: []string{"Amazon S3"}},
		{name: "over a day in the future", body: servicesBody(1, future, `["Amazon S3"]`)},
		{name: "unparseable generatedAt", body: servicesBody(1, "yesterday", `["Amazon S3"]`)},
		{name: "filters bad entries", body: servicesBody(1, fresh, `["", "  ", 7, null, "`+long+`", " Amazon  EC2 "]`), want: []string{"Amazon EC2"}},
		{name: "caps at fifteen", body: servicesBody(1, fresh, many), want: strings.Fields(strings.Repeat("S ", 15))},
		{name: "oversized body", body: servicesBody(1, fresh, `["`+strings.Repeat("a", 70*1024)+`"]`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeServicesS3{body: tc.body, err: tc.err}
			bucket := "billing-bucket"
			if tc.bucket == "-" {
				bucket = ""
			}
			got, err := newTestServicesReader(f, bucket).ServicesInUse(context.Background(), "acct-1")
			if tc.wantE != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantE)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") || len(got) != len(tc.want) {
				t.Fatalf("services = %q, want %q", got, tc.want)
			}
			if bucket == "" {
				if len(f.keys) != 0 {
					t.Fatalf("unset bucket still read S3: %v", f.keys)
				}
				return
			}
			if len(f.keys) != 1 || f.keys[0] != "accounts/acct-1/derived/services-in-use.json" || f.bucks[0] != "billing-bucket" {
				t.Fatalf("read %v from %v, want exactly the derived object", f.keys, f.bucks)
			}
		})
	}
}

func TestServicesInUseReaderRejectsInvalidAccountID(t *testing.T) {
	for _, id := range []string{"", "../acct", "acct/1", "a b", strings.Repeat("a", 65), "acct%2F1"} {
		f := &fakeServicesS3{body: "{}"}
		got, err := newTestServicesReader(f, "billing-bucket").ServicesInUse(context.Background(), id)
		if !errors.Is(err, ErrInvalidInput) || got != nil {
			t.Fatalf("id %q: got %v, %v; want ErrInvalidInput", id, got, err)
		}
		if len(f.keys) != 0 {
			t.Fatalf("id %q reached S3: %v", id, f.keys)
		}
	}
}
