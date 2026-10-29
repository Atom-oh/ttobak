package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	if req.OwnerEmail != "" && !middleware.GetEmailVerified(ctx) {
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "이메일을 인증한 뒤 Owner를 초대하세요.")
		return
	}
	acc, err := h.accountService.CreateAccountWithOwner(ctx, userID, email, &req)
	if err != nil {
		writeAccountCreationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, acc)
}

func (h *AccountHandler) ResendOwnerInvitation(w http.ResponseWriter, r *http.Request) {
	if !middleware.GetEmailVerified(r.Context()) {
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "이메일을 인증한 뒤 초대를 재전송하세요.")
		return
	}
	var body *struct{}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body == nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	member, err := h.accountService.ResendOwnerInvitation(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"))
	if err != nil {
		writeAccountCreationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, member)
}

func writeAccountCreationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInviteEmail):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "유효한 이메일 주소를 입력하세요.")
	case errors.Is(err, service.ErrInvitationGrantFailed):
		writeError(w, http.StatusInternalServerError, model.ErrCodeInvitationGrantFailed, "초대 메일이 이미 요청되었을 수 있지만 Account 생성 또는 연결을 확인하지 못했습니다. 같은 입력으로 다시 시도하세요.")
	case errors.Is(err, service.ErrInvitationRejected):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "초대할 수 없는 이메일입니다. 허용된 이메일 도메인을 확인하세요.")
	case errors.Is(err, service.ErrInvitationRateLimited):
		writeError(w, http.StatusTooManyRequests, model.ErrCodeRateLimited, "초대 요청이 많습니다. 한 시간 후 다시 시도하세요.")
	case errors.Is(err, service.ErrInvitationNotPending):
		writeError(w, http.StatusConflict, model.ErrCodeConflict, "이미 가입한 사용자에게는 초대 메일을 재발송할 수 없습니다.")
	case errors.Is(err, service.ErrInvitationUnavailable):
		writeError(w, http.StatusServiceUnavailable, model.ErrCodeInternalError, "초대 서비스를 사용할 수 없습니다. 잠시 후 다시 시도하세요.")
	default:
		writeAccountHierarchyError(w, err)
	}
}

func (h *AccountHandler) UpdateAccountParent(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID is required")
		return
	}
	var req model.UpdateAccountParentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	if req.ParentAccountID == nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "parentAccountId is required (empty string detaches)")
		return
	}
	resp, err := h.accountService.UpdateAccountParent(r.Context(), middleware.GetUserID(r.Context()), accountID, &req)
	if err != nil {
		writeAccountHierarchyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeAccountHierarchyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid account name or parent hierarchy")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Account or parent not found")
	case errors.Is(err, service.ErrAccountHierarchyConflict):
		writeError(w, http.StatusConflict, model.ErrCodeConflict, "Account hierarchy changed; retry the request")
	default:
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
	}
}

func (h *AccountHandler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	if len(r.URL.Query()) > 0 {
		page, err := h.accountService.ListAccountsPage(ctx, userID, r.URL.Query())
		if err != nil {
			switch {
			case errors.Is(err, service.ErrInvalidReadingOptions), errors.Is(err, service.ErrInvalidReadingCursor):
				writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid account list options")
			case errors.Is(err, service.ErrStaleReadingCursor):
				writeError(w, http.StatusConflict, model.ErrCodeConflict, "Account access changed; restart the list")
			default:
				writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
			}
			return
		}
		writeJSON(w, http.StatusOK, page)
		return
	}
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
	if !middleware.GetEmailVerified(ctx) {
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "이메일을 인증한 뒤 멤버를 초대하세요.")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	dto, err := h.accountService.AddMember(ctx, userID, accountID, &req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidInviteEmail):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "유효한 이메일 주소를 입력하세요.")
		case errors.Is(err, service.ErrInvitationRejected):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "초대할 수 없는 이메일입니다. 허용된 이메일 도메인을 확인하세요.")
		case errors.Is(err, service.ErrInvitationNotPending):
			writeError(w, http.StatusConflict, model.ErrCodeBadRequest, "이미 가입한 사용자에게는 초대 메일을 재발송할 수 없습니다.")
		case errors.Is(err, service.ErrInvitationRateLimited):
			writeError(w, http.StatusTooManyRequests, model.ErrCodeRateLimited, "초대 요청이 많습니다. 한 시간 후 다시 시도하세요.")
		case errors.Is(err, service.ErrInvitationGrantFailed):
			writeError(w, http.StatusInternalServerError, model.ErrCodeInvitationGrantFailed, "Account 연결을 예약하지 못했습니다. 다시 추가하세요.")
		case errors.Is(err, service.ErrInvitationUnavailable):
			writeError(w, http.StatusServiceUnavailable, model.ErrCodeInternalError, "초대 서비스를 사용할 수 없습니다. 잠시 후 다시 시도하세요.")
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Only account members can add members")
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
		case errors.Is(err, service.ErrPendingAlreadyClaimed):
			writeError(w, http.StatusConflict, model.ErrCodeConflict, "This invite was already claimed -- remove the user from Members instead")
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
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Only account members can change member roles")
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
		case errors.Is(err, service.ErrSummaryReadRestricted):
			writeError(w, http.StatusConflict, "SHARING_REGEN_REQUIRED", "The owner must regenerate sharing-safe meeting notes before these insights can be read")
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
		case errors.Is(err, service.ErrSummaryReadRestricted):
			writeError(w, http.StatusConflict, "SHARING_REGEN_REQUIRED", "The owner must regenerate sharing-safe meeting notes before these insights can be read")
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
