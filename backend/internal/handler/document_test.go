package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

func newStubDocumentHandler() (*DocumentHandler, *mockHandlerAccountRepo) {
	repo := newMockHandlerAccountRepo()
	svc := service.NewAccountServiceForTest(repo)
	return &DocumentHandler{accountService: svc}, repo
}

func TestHandlerPutUserDocument_Created(t *testing.T) {
	h, _ := newStubDocumentHandler()
	body, _ := json.Marshal(model.PutDocumentRequest{Title: "My Note", Markdown: mdPtr("personal content")})
	r := httptest.NewRequest(http.MethodPost, "/api/documents", bytes.NewReader(body))
	r = withUserEmailCtx(r, "user-1", "u@x.com")
	w := httptest.NewRecorder()

	h.PutDocument(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", w.Code, w.Body.String())
	}
	var dto model.AccountDocumentDTO
	json.Unmarshal(w.Body.Bytes(), &dto)
	if dto.DocID == "" || dto.Title != "My Note" {
		t.Errorf("unexpected dto: %+v", dto)
	}
}

func TestHandlerPersonalDocument_UpdateAndDelete_NoMembershipNeeded(t *testing.T) {
	h, _ := newStubDocumentHandler()
	createBody, _ := json.Marshal(model.PutDocumentRequest{Title: "Note", Markdown: mdPtr("v1")})
	createReq := httptest.NewRequest(http.MethodPost, "/api/documents", bytes.NewReader(createBody))
	createReq = withUserEmailCtx(createReq, "user-1", "u@x.com")
	createW := httptest.NewRecorder()
	h.PutDocument(createW, createReq)
	var created model.AccountDocumentDTO
	json.Unmarshal(createW.Body.Bytes(), &created)

	updateBody, _ := json.Marshal(model.PutDocumentRequest{Title: "Note v2", Markdown: mdPtr("v2")})
	updateReq := httptest.NewRequest(http.MethodPut, "/api/documents/"+created.DocID, bytes.NewReader(updateBody))
	updateReq = withUserEmailCtx(updateReq, "user-1", "u@x.com")
	updateReq = withChiParam(updateReq, "docId", created.DocID)
	updateW := httptest.NewRecorder()
	h.UpdateDocument(updateW, updateReq)
	if updateW.Code != http.StatusOK {
		t.Fatalf("expected 200 on update, got %d (%s)", updateW.Code, updateW.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/documents/"+created.DocID, nil)
	deleteReq = withUserEmailCtx(deleteReq, "user-1", "u@x.com")
	deleteReq = withChiParam(deleteReq, "docId", created.DocID)
	deleteW := httptest.NewRecorder()
	h.DeleteDocument(deleteW, deleteReq)
	if deleteW.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on delete, got %d (%s)", deleteW.Code, deleteW.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/documents/"+created.DocID, nil)
	getReq = withUserEmailCtx(getReq, "user-1", "u@x.com")
	getReq = withChiParam(getReq, "docId", created.DocID)
	getW := httptest.NewRecorder()
	h.GetDocument(getW, getReq)
	if getW.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d (%s)", getW.Code, getW.Body.String())
	}
}
