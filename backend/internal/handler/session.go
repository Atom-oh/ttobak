package handler

import (
	"context"
	"io"
	"log"
	"net/http"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type sessionBootstrapper interface {
	BootstrapSession(context.Context, string, string, string, bool) (*service.SessionBootstrapResponse, error)
}
type SessionHandler struct{ service sessionBootstrapper }

func NewSessionHandler(s sessionBootstrapper) *SessionHandler { return &SessionHandler{service: s} }
func (h *SessionHandler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if middleware.GetUserID(ctx) == "" {
		writeError(w, http.StatusUnauthorized, model.ErrCodeUnauthorized, "Authentication required")
		return
	}
	if _, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, 1024)); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Unexpected request body")
		return
	}
	// All identity comes from verified middleware claims, never a request body.
	result, err := h.service.BootstrapSession(ctx, middleware.GetUserID(ctx), middleware.GetUserEmail(ctx), middleware.GetUserName(ctx), middleware.GetEmailVerified(ctx))
	if err != nil {
		log.Printf("session bootstrap failed: %v", err)
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "로그인 후 계정 연결을 완료하지 못했습니다. 다시 시도해주세요.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
