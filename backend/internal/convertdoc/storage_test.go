package convertdoc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type previewObjects struct {
	sourceETag, sourceVersion, destinationETag string
	getBody                                    string
	put                                        *s3.PutObjectInput
	putError                                   error
}

func (m *previewObjects) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if *in.Key == "docs/user/deck.pptx" {
		return &s3.HeadObjectOutput{ETag: aws.String(m.sourceETag), VersionId: aws.String(m.sourceVersion)}, nil
	}
	if m.destinationETag == "" {
		return nil, &types.NotFound{}
	}
	return &s3.HeadObjectOutput{ETag: aws.String(m.destinationETag)}, nil
}
func (m *previewObjects) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return &s3.GetObjectOutput{
		ETag: aws.String(m.sourceETag), VersionId: aws.String(m.sourceVersion),
		ContentLength: aws.Int64(int64(len(m.getBody))), Body: io.NopCloser(bytes.NewBufferString(m.getBody)),
	}, nil
}
func (m *previewObjects) PutObject(_ context.Context, in *s3.PutObjectInput, options ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	m.put = in
	return &s3.PutObjectOutput{}, m.putError
}

func TestPreviewBindsExactDownloadedSourceAndDestination(t *testing.T) {
	for _, prior := range []string{"", `"existing"`} {
		t.Run(prior, func(t *testing.T) {
			m := &previewObjects{sourceETag: `"source"`, sourceVersion: "opaque/version", destinationETag: prior, getBody: "slides"}
			store := NewPreviewStorage(m, "assets")
			destination, err := store.Snapshot(context.Background(), "docs-pdf/user/deck.pptx.pdf")
			if err != nil {
				t.Fatal(err)
			}
			var downloaded bytes.Buffer
			source, err := store.Download(context.Background(), "docs/user/deck.pptx", &downloaded)
			if err != nil || downloaded.String() != "slides" {
				t.Fatalf("download: %q, %v", downloaded.String(), err)
			}
			if err := store.Publish(context.Background(), source, destination, bytes.NewReader([]byte("pdf"))); err != nil {
				t.Fatal(err)
			}
			if m.put.Metadata["source-etag"] != `"source"` || m.put.Metadata["source-version-id"] != "opaque/version" {
				t.Fatalf("missing binding: %#v", m.put)
			}
			if prior == "" && aws.ToString(m.put.IfNoneMatch) != "*" {
				t.Fatal("missing create condition")
			}
			if prior != "" && aws.ToString(m.put.IfMatch) != prior {
				t.Fatal("missing replace condition")
			}
		})
	}
}

func TestChangedSourceNeverPublishes(t *testing.T) {
	for _, change := range []string{"etag", "version"} {
		t.Run(change, func(t *testing.T) {
			m := &previewObjects{sourceETag: `"source"`, sourceVersion: "v1", getBody: "slides"}
			store := NewPreviewStorage(m, "assets")
			source, err := store.Download(context.Background(), "docs/user/deck.pptx", io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if change == "etag" {
				m.sourceETag = `"new"`
			} else {
				m.sourceVersion = "v2"
			}
			err = store.Publish(context.Background(), source, PreviewDestination{Key: "docs-pdf/user/deck.pptx.pdf"}, bytes.NewReader(nil))
			if !errors.Is(err, ErrPreviewSourceChanged) || m.put != nil {
				t.Fatalf("published changed source: %v", err)
			}
		})
	}
}

func TestPreviewWriteFailurePreservesExistingObject(t *testing.T) {
	m := &previewObjects{sourceETag: `"source"`, getBody: "slides", putError: errors.New("unknown write outcome")}
	store := NewPreviewStorage(m, "assets")
	source, err := store.Download(context.Background(), "docs/user/deck.pptx", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Publish(context.Background(), source, PreviewDestination{Key: "docs-pdf/user/deck.pptx.pdf", ETag: `"old"`}, bytes.NewReader(nil)); !errors.Is(err, m.putError) {
		t.Fatalf("failure swallowed: %v", err)
	}
}

func TestPreviewSDKWirePreservesBindingAndWriteCondition(t *testing.T) {
	var puts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "HEAD" && r.URL.Path == "/assets/docs-pdf/user/deck.pptx.pdf":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == "GET" && r.URL.Path == "/assets/docs/user/deck.pptx":
			w.Header().Set("ETag", `"source"`)
			w.Header().Set("x-amz-version-id", "source-version")
			w.Header().Set("Content-Length", "6")
			_, _ = io.WriteString(w, "slides")
		case r.Method == "HEAD" && r.URL.Path == "/assets/docs/user/deck.pptx":
			w.Header().Set("ETag", `"source"`)
			w.Header().Set("x-amz-version-id", "source-version")
		case r.Method == "PUT":
			puts++
			if r.URL.Path != "/assets/docs-pdf/user/deck.pptx.pdf" ||
				r.Header.Get("If-None-Match") != "*" ||
				r.Header.Get("x-amz-meta-source-etag") != `"source"` ||
				r.Header.Get("x-amz-meta-source-version-id") != "source-version" {
				t.Errorf("incorrect publication: %s %#v", r.URL.Path, r.Header)
			}
			// A retry after this ambiguous response must not happen here.
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "<Error><Code>ServiceUnavailable</Code></Error>")
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	client := s3.NewFromConfig(aws.Config{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}}, func(o *s3.Options) {
		o.BaseEndpoint, o.UsePathStyle = aws.String(server.URL), true
	})
	storage := NewPreviewStorage(client, "assets")
	destination, err := storage.Snapshot(context.Background(), "docs-pdf/user/deck.pptx.pdf")
	if err != nil {
		t.Fatal(err)
	}
	source, err := storage.Download(context.Background(), "docs/user/deck.pptx", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	err = storage.Publish(context.Background(), source, destination, bytes.NewReader([]byte("pdf")))
	if err == nil || puts != 1 {
		t.Fatalf("uncertain PUT: calls=%d error=%v", puts, err)
	}
}
