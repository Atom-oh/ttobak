package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type AccountHandler struct {
	accountService *service.AccountService
	uploadService  *service.UploadService
}

func NewAccountHandler(accountService *service.AccountService, uploadService *service.UploadService) *AccountHandler {
	return &AccountHandler{accountService: accountService, uploadService: uploadService}
}

// withDownloadURL presigns detail.FileKey (if set -- a slide doc) into
// DownloadURL before the handler serializes the response. Presign failure
// is logged and swallowed: the caller still gets the doc metadata, just
// without a working download link, rather than a 500 for an unrelated S3
// hiccup.
func (h *AccountHandler) withDownloadURL(ctx context.Context, detail *model.AccountDocumentDetail) *model.AccountDocumentDetail {
	if detail.FileKey == "" {
		return detail
	}
	url, err := h.uploadService.GeneratePresignedDownloadURL(ctx, detail.FileKey)
	if err != nil {
		log.Printf("presign download URL for doc %s: %v", detail.DocID, err)
		return detail
	}
	detail.DownloadURL = url
	if previewURL, err := h.uploadService.GeneratePreviewPDFURL(ctx, detail.FileKey); err != nil {
		log.Printf("presign preview URL for doc %s: %v", detail.DocID, err)
	} else {
		detail.PreviewURL = previewURL
	}
	return detail
}

func (h *AccountHandler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	email := middleware.GetUserEmail(ctx)

	var req model.CreateAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	acc, err := h.accountService.CreateAccount(ctx, userID, email, &req)
	if err != nil {
		if errors.Is(err, service.ErrInvalidInput) {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account name is required")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}
	// Return the freshly-created account as a response (owner is the only member).
	writeJSON(w, http.StatusCreated, model.AccountResponse{
		AccountID:   acc.AccountID,
		Name:        acc.Name,
		Aliases:     acc.Aliases,
		Domains:     acc.Domains,
		Industry:    acc.Industry,
		OwnerUserID: acc.OwnerUserID,
		Members:     []model.AccountMemberDTO{{UserID: userID, Email: email, Role: model.RoleOwner}},
		CreatedAt:   acc.CreatedAt,
	})
}

func (h *AccountHandler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	list, err := h.accountService.ListAccounts(ctx, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"accounts": list})
}

func (h *AccountHandler) GetAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID is required")
		return
	}
	resp, err := h.accountService.GetAccount(ctx, userID, accountID)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
			return
		}
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Account not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *AccountHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID is required")
		return
	}
	var req model.AddMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	dto, err := h.accountService.AddMember(ctx, userID, accountID, &req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Only the owner can add members")
		case errors.Is(err, service.ErrUserNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "No user with that email")
		case errors.Is(err, service.ErrMemberExists):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "User is already a member")
		case errors.Is(err, service.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid role ("+strings.Join(model.AssignableRoles, "/")+")")
		case errors.Is(err, service.ErrSelfShare):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Cannot add yourself as a member")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

// RevokePendingMember handles DELETE /api/accounts/{accountId}/members/pending?email={email} --
// cancels a queued PendingShare account invite before the target has ever
// logged in (see AccountService.RevokePendingMember's doc comment for why
// this is a separate route from RemoveMember rather than reusing its
// {userId} path: there's no userId yet for a grant that's never been
// claimed).
func (h *AccountHandler) RevokePendingMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	email := r.URL.Query().Get("email")

	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID is required")
		return
	}
	if email == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "email query parameter is required")
		return
	}

	if err := h.accountService.RevokePendingMember(ctx, userID, accountID, email); err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Only the owner can revoke a pending invite")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Account not found")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AccountHandler) UpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	targetUserID := chi.URLParam(r, "userId")
	if accountID == "" || targetUserID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID and user ID are required")
		return
	}
	var req model.UpdateMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	dto, err := h.accountService.UpdateMemberRole(ctx, userID, accountID, targetUserID, &req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Only the owner can change member roles")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Member not found")
		case errors.Is(err, service.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid role ("+strings.Join(model.AssignableRoles, "/")+") or target is the owner")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (h *AccountHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	targetUserID := chi.URLParam(r, "userId")
	if accountID == "" || targetUserID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID and user ID are required")
		return
	}
	force := r.URL.Query().Get("force") == "true"
	result, err := h.accountService.RemoveMember(ctx, userID, accountID, targetUserID, force)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Only the owner can remove members")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Member not found")
		case errors.Is(err, service.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "The owner cannot be removed")
		case errors.Is(err, service.ErrAmbiguousShareBlocksRemoval):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "This member holds an untagged share that may be a direct grant or a legacy account-share; pass ?force=true to remove anyway (the share will be left untouched and reported in the response)")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	// The membership removal itself always succeeds at this point -- 204 is
	// reserved for the fully-clean case (no body, matching HTTP semantics);
	// when Share cleanup failed or found an ambiguous untagged Share, surface
	// it in a 200 body instead of leaving the caller without a signal.
	if len(result.FailedMeetingIDs) > 0 || len(result.AmbiguousUntaggedMeetingIDs) > 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"removed":                     true,
			"cleanupFailedForMeetings":    result.FailedMeetingIDs,
			"ambiguousUntaggedMeetingIDs": result.AmbiguousUntaggedMeetingIDs,
		})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AccountHandler) ListAccountMeetings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID is required")
		return
	}
	list, err := h.accountService.ListAccountMeetings(ctx, userID, accountID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Account not found")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"meetings": list})
}

func (h *AccountHandler) ListAccountInsights(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID is required")
		return
	}
	var from, to time.Time
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid 'from' (RFC3339)")
			return
		}
		from = t
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid 'to' (RFC3339)")
			return
		}
		to = t
	}
	var types []string
	if v := r.URL.Query().Get("types"); v != "" {
		for _, t := range strings.Split(v, ",") {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			if !model.IsValidInsightType(t) {
				writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid insight type: "+t)
				return
			}
			types = append(types, t)
		}
	}
	list, err := h.accountService.ListAccountInsights(ctx, userID, accountID, from, to, types)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Account not found")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"insights": list})
}

func (h *AccountHandler) GetAccountBrief(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID is required")
		return
	}
	var from, to time.Time
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid 'from' (RFC3339)")
			return
		}
		from = t
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid 'to' (RFC3339)")
			return
		}
		to = t
	}
	var types []string
	if v := r.URL.Query().Get("types"); v != "" {
		for _, t := range strings.Split(v, ",") {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			if !model.IsValidInsightType(t) {
				writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid insight type: "+t)
				return
			}
			types = append(types, t)
		}
	}
	brief, err := h.accountService.GetAccountBrief(ctx, userID, accountID, from, to, types)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Account not found")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, brief)
}

func (h *AccountHandler) PutDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID is required")
		return
	}
	var req model.PutDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	dto, err := h.accountService.PutDocument(ctx, userID, accountID, &req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Account not found")
		case errors.Is(err, service.ErrLoopGuard):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document originated from TTOBAK (loop guard)")
		case errors.Is(err, service.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "title and markdown required (<=300KB)")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (h *AccountHandler) ListDocuments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID is required")
		return
	}
	list, err := h.accountService.ListAccountDocuments(ctx, userID, accountID, r.URL.Query().Get("docType"))
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Account not found")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"documents": list})
}

func (h *AccountHandler) GetDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	docID := chi.URLParam(r, "docId")
	if accountID == "" || docID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID and Document ID are required")
		return
	}
	detail, err := h.accountService.GetAccountDocument(ctx, userID, accountID, docID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Document not found")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, h.withDownloadURL(ctx, detail))
}

func (h *AccountHandler) UpdateDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	docID := chi.URLParam(r, "docId")
	if accountID == "" || docID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID and Document ID are required")
		return
	}
	var req model.PutDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	dto, err := h.accountService.UpdateAccountDocument(ctx, userID, accountID, docID, &req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Document not found")
		case errors.Is(err, service.ErrLoopGuard):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document originated from TTOBAK (loop guard)")
		case errors.Is(err, service.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "title required, markdown <=300KB")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (h *AccountHandler) DeleteDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	docID := chi.URLParam(r, "docId")
	if accountID == "" || docID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID and Document ID are required")
		return
	}
	if err := h.accountService.DeleteAccountDocument(ctx, userID, accountID, docID); err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
		case errors.Is(err, service.ErrNotFound):
			// ErrNotFound here means either the account (requireMember) or the
			// document itself (the delete's conditional-check) doesn't exist --
			// "Document not found" matches UpdateDocument/API-SPEC's wording.
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Document not found")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
