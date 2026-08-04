package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// DocumentHandler serves personal (account-less) documents -- PK: USER#{userId}.
// Ownership is implicit in the PK (always built from the authenticated
// userID), so unlike AccountHandler's document methods there's no membership
// check -- but ErrNotFound (missing doc) and ErrForbidden (a fileKey not
// prefixed docs/{userID}/) can still occur and map to 404/403 as usual.
type DocumentHandler struct {
	accountService *service.AccountService
	uploadService  *service.UploadService
}

func NewDocumentHandler(accountService *service.AccountService, uploadService *service.UploadService) *DocumentHandler {
	return &DocumentHandler{accountService: accountService, uploadService: uploadService}
}

func (h *DocumentHandler) PutDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	var req model.PutDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	dto, err := h.accountService.PutUserDocument(ctx, userID, &req)
	if err != nil {
		writeDocumentServiceError(w, err, "title and markdown required (<=300KB)")
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (h *DocumentHandler) ListDocuments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	list, err := h.accountService.ListUserDocuments(ctx, userID, r.URL.Query().Get("docType"))
	if err != nil {
		writeDocumentServiceError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"documents": list})
}

func (h *DocumentHandler) GetDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	docID := chi.URLParam(r, "docId")
	if docID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document ID is required")
		return
	}
	detail, err := h.accountService.GetUserDocument(ctx, userID, docID)
	if err != nil {
		writeDocumentServiceError(w, err, "")
		return
	}
	if detail.FileKey != "" {
		// Presign failure is logged and swallowed, matching account.go's
		// withDownloadURL -- the caller still gets the doc metadata, just
		// without a working download/preview link, rather than a 500 for
		// an unrelated S3 hiccup.
		if url, presignErr := h.uploadService.GeneratePresignedDownloadURL(ctx, detail.FileKey); presignErr != nil {
			log.Printf("presign download URL for doc %s: %v", detail.DocID, presignErr)
		} else {
			detail.DownloadURL = url
		}
		if previewURL, err := h.uploadService.GeneratePreviewPDFURL(ctx, detail.FileKey); err != nil {
			log.Printf("presign preview URL for doc %s: %v", detail.DocID, err)
		} else {
			detail.PreviewURL = previewURL
		}
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *DocumentHandler) UpdateDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	docID := chi.URLParam(r, "docId")
	if docID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document ID is required")
		return
	}
	var req model.PutDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	dto, err := h.accountService.UpdateUserDocument(ctx, userID, docID, &req)
	if err != nil {
		writeDocumentServiceError(w, err, "title required, markdown <=300KB")
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (h *DocumentHandler) DeleteDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	docID := chi.URLParam(r, "docId")
	if docID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document ID is required")
		return
	}
	if err := h.accountService.DeleteUserDocument(ctx, userID, docID); err != nil {
		writeDocumentServiceError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ShareToAccount handles POST /api/documents/{docId}/share-account — copies
// a personal document into an account's document list (see
// AccountService.ShareUserDocumentToAccount for why it copies rather than links).
func (h *DocumentHandler) ShareToAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	docID := chi.URLParam(r, "docId")
	if docID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document ID is required")
		return
	}
	var req model.ShareToAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AccountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "accountId is required")
		return
	}
	dto, err := h.accountService.ShareUserDocumentToAccount(ctx, userID, docID, req.AccountID)
	if err != nil {
		writeDocumentServiceError(w, err, "")
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

// ShareWithUser handles POST /api/documents/{docId}/share — shares a personal
// document with one other user by REFERENCE (read-only), unlike
// ShareToAccount's copy. Mirrors ShareMeeting's request/response shape minus
// `permission`, which a document share doesn't accept.
func (h *DocumentHandler) ShareWithUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	userEmail := middleware.GetUserEmail(ctx)
	docID := chi.URLParam(r, "docId")
	if docID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document ID is required")
		return
	}
	var req model.ShareDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "email is required")
		return
	}
	share, err := h.accountService.ShareUserDocumentByEmail(ctx, userID, userEmail, docID, req.Email)
	if err != nil {
		writeDocumentServiceError(w, err, "")
		return
	}
	writeJSON(w, http.StatusCreated, model.SharedWithResponse{SharedWith: model.ShareResponse{
		UserID: share.SharedToID, Email: share.Email, Permission: share.Permission,
	}})
}

// RevokeShare handles DELETE /api/documents/{docId}/share/{userId}.
func (h *DocumentHandler) RevokeShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	docID := chi.URLParam(r, "docId")
	targetUserID := chi.URLParam(r, "userId")
	if docID == "" || targetUserID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document ID and user ID are required")
		return
	}
	if err := h.accountService.RevokeUserDocShare(ctx, userID, docID, targetUserID); err != nil {
		writeDocumentServiceError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListShares handles GET /api/documents/{docId}/shares (owner only).
func (h *DocumentHandler) ListShares(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	docID := chi.URLParam(r, "docId")
	if docID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document ID is required")
		return
	}
	shares, err := h.accountService.ListUserDocShares(ctx, userID, docID)
	if err != nil {
		writeDocumentServiceError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"shares": shares})
}

// CreatePublicShare handles POST /api/documents/{docId}/public-share —
// mints an unauthenticated share link for a personal file-backed document
// (gated on FileKey != "" in AccountService.CreateUserDocPublicShare, not
// docType == "slide" specifically; in practice the only file-backed
// personal docType is "slide").
func (h *DocumentHandler) CreatePublicShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	docID := chi.URLParam(r, "docId")
	if docID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document ID is required")
		return
	}
	token, err := h.accountService.CreateUserDocPublicShare(ctx, userID, docID)
	if err != nil {
		writeDocumentServiceError(w, err, "파일이 첨부된 문서만 공개 공유할 수 있습니다")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

// RevokePublicShare handles DELETE /api/documents/{docId}/public-share.
func (h *DocumentHandler) RevokePublicShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	docID := chi.URLParam(r, "docId")
	if docID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document ID is required")
		return
	}
	if err := h.accountService.RevokeUserDocPublicShare(ctx, userID, docID); err != nil {
		writeDocumentServiceError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PublicGetDoc handles GET /api/public/docs/{token} — the one route
// registered under a CloudFront behavior with no Lambda@Edge JWT check
// and no API Gateway authorizer (see cmd/api/main.go's route registration
// comment and infra/lib/gateway-stack.ts / frontend-stack.ts for how it
// bypasses both). It never returns document content directly, only a 302
// redirect to a short-lived presigned S3 URL, and never trusts a
// caller-identity header of any kind.
func (h *DocumentHandler) PublicGetDoc(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := chi.URLParam(r, "token")
	if token == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "token is required")
		return
	}
	doc, err := h.accountService.ResolvePublicShare(ctx, token)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "공개 링크를 찾을 수 없습니다")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "internal error")
		return
	}

	// Short-lived (PublicShareURLTTL, not the usual 1-hour default): this URL
	// leaks to an unauthenticated caller, so a revoke should close the window
	// quickly rather than leaving it valid for an hour (ADR-022).
	targetKey := doc.FileKey
	if sidecarKey := service.SidecarPDFKey(doc.FileKey); sidecarKey != "" {
		previewURL, err := h.uploadService.GeneratePreviewPDFURLShortLived(ctx, doc.FileKey)
		if err != nil || previewURL == "" {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "PDF로 변환 중입니다. 잠시 후 다시 시도해 주세요")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, previewURL, http.StatusFound)
		return
	}

	url, err := h.uploadService.GeneratePresignedDownloadURLWithTTL(ctx, targetKey, service.PublicShareURLTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "internal error")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, url, http.StatusFound)
}

// writeDocumentServiceError maps the account-document sentinel errors shared
// by both AccountHandler and DocumentHandler's document methods to HTTP
// responses. invalidInputMsg is shown for ErrInvalidInput (its meaning
// differs between create/list/update).
func writeDocumentServiceError(w http.ResponseWriter, err error, invalidInputMsg string) {
	switch {
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Document not found")
	case errors.Is(err, service.ErrUserNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "사용자를 찾을 수 없습니다")
	case errors.Is(err, service.ErrSelfShare):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "자신에게는 공유할 수 없습니다")
	case errors.Is(err, service.ErrLoopGuard):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document originated from TTOBAK (loop guard)")
	case errors.Is(err, service.ErrInvalidInput):
		msg := invalidInputMsg
		if msg == "" {
			msg = "invalid input"
		}
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, msg)
	default:
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
	}
}
