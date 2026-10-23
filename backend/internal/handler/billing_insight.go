package handler

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type billingHandoffCreator interface {
	Enabled() bool
	CreateHandoff(ctx context.Context, sub, email string) (string, error)
}

// BillingInsightHandler serves POST /api/billing-insight/handoff.
type BillingInsightHandler struct{ service billingHandoffCreator }

func NewBillingInsightHandler(s billingHandoffCreator) *BillingInsightHandler {
	return &BillingInsightHandler{service: s}
}

// maxBillingHandoffBody bounds the ignored request body.
const maxBillingHandoffBody = 1024

// CreateHandoff mints a 60-second single-use code for the companion dashboard.
// Identity comes only from verified middleware claims; any request body is
// ignored apart from its size.
func (h *BillingInsightHandler) CreateHandoff(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.service == nil || !h.service.Enabled() {
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Not found")
		return
	}
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	if userID == "" {
		writeError(w, http.StatusUnauthorized, model.ErrCodeUnauthorized, "Authentication required")
		return
	}
	email := middleware.GetUserEmail(ctx)
	if email == "" {
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "이메일이 확인된 로그인만 빌링 인사이트를 열 수 있습니다.")
		return
	}
	if verified, ok := ctx.Value(middleware.UserEmailVerifiedKey).(bool); ok && !verified {
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "이메일이 확인된 로그인만 빌링 인사이트를 열 수 있습니다.")
		return
	}
	if r.Body != nil {
		if _, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxBillingHandoffBody)); err != nil {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid hand-off request")
			return
		}
	}
	target, err := h.service.CreateHandoff(ctx, userID, email)
	switch {
	case errors.Is(err, service.ErrBillingInsightDisabled):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Not found")
		return
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "이메일이 확인된 로그인만 빌링 인사이트를 열 수 있습니다.")
		return
	case err != nil:
		log.Printf("billing insight hand-off failed: %v", err)
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "빌링 인사이트를 열지 못했습니다. 다시 시도해주세요.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": target})
}
