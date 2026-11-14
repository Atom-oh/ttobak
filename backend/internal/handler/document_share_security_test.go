package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

func TestIntentionalDocumentSharingAndRevocation(t *testing.T) {
	repo := newMockHandlerAccountRepo()
	accountService := service.NewAccountServiceForTest(repo)
	client := s3.New(s3.Options{Region: "ap-northeast-2", Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""))})
	handler := NewDocumentHandler(accountService, service.NewUploadService(client, nil, "test-bucket"))
	pk := model.PrefixUser + "owner"
	repo.documents[pk] = []model.AccountDocument{{PK: pk, DocID: "doc", FileKey: "docs/owner/고객 자료.pdf"}}

	documentRequest := func(user string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.GetDocument(w, withChiParam(withUserCtx(httptest.NewRequest(http.MethodGet, "/", nil), user), "docId", "doc"))
		return w
	}
	if w := documentRequest("reader"); w.Code != http.StatusNotFound {
		t.Fatalf("unshared document accessible: status=%d", w.Code)
	}
	repo.docShares[handlerDocShareKey("reader", "doc")] = &model.Share{MeetingID: "doc", OwnerID: "owner", SharedToID: "reader", Permission: model.PermissionRead}
	if w := documentRequest("reader"); w.Code != http.StatusOK {
		t.Fatalf("intended email share failed: status=%d body=%s", w.Code, w.Body)
	} else {
		var detail model.AccountDocumentDetail
		if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || detail.DownloadURL == "" {
			t.Fatalf("shared document missing download URL: err=%v", err)
		}
	}
	delete(repo.docShares, handlerDocShareKey("reader", "doc"))
	if w := documentRequest("reader"); w.Code != http.StatusNotFound {
		t.Fatalf("revoked email share accessible: status=%d", w.Code)
	}

	token, err := accountService.CreateUserDocPublicShare(t.Context(), "owner", "doc")
	if err != nil {
		t.Fatal(err)
	}
	publicRequest := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.PublicGetDoc(w, withChiParam(httptest.NewRequest(http.MethodGet, "/", nil), "token", token))
		return w
	}
	w := publicRequest()
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != http.StatusFound || location.Query().Get("X-Amz-Expires") != "300" {
		t.Fatalf("public share failed or expiry changed: status=%d location=%q err=%v", w.Code, w.Header().Get("Location"), err)
	}
	if err := accountService.RevokeUserDocPublicShare(t.Context(), "owner", "doc"); err != nil {
		t.Fatal(err)
	}
	if w := publicRequest(); w.Code != http.StatusNotFound {
		t.Fatalf("revoked public share accessible: status=%d", w.Code)
	}
}
