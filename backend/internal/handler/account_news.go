package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type accountNewsAPI interface {
	Create(ctx context.Context, userID, accountID string, req *model.CreateAccountNewsSourceRequest) (*model.CrawlerSource, error)
	List(ctx context.Context, userID, accountID string) ([]model.CrawlerSource, error)
	Delete(ctx context.Context, userID, accountID, sourceID string) error
	Update(ctx context.Context, userID, accountID, sourceID string, req *model.UpdateAccountNewsSourceRequest) (*model.NewsSourceItem, error)
	ListAll(ctx context.Context, userID, cursor string, limit int) (*model.NewsSourcesResponse, error)
	CollectNow(ctx context.Context, userID, accountID, sourceID string) (*model.NewsCollectResponse, error)
}

// AccountNewsHandler serves account-owned news sources (direct members only).
type AccountNewsHandler struct{ service accountNewsAPI }

func NewAccountNewsHandler(s accountNewsAPI) *AccountNewsHandler {
	return &AccountNewsHandler{service: s}
}

func accountNewsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
	case errors.Is(err, service.ErrAccountMerging):
		writeAccountMerging(w)
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "News source not found")
	default:
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Internal server error")
	}
}

func (h *AccountNewsHandler) List(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		accountNewsError(w, service.ErrInvalidInput)
		return
	}
	sources, err := h.service.List(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"))
	if err != nil {
		accountNewsError(w, err)
		return
	}
	if sources == nil {
		sources = []model.CrawlerSource{}
	}
	writeJSON(w, http.StatusOK, model.AccountNewsSourcesResponse{Sources: sources})
}

func (h *AccountNewsHandler) Create(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var req model.CreateAccountNewsSourceRequest
	if err := decoder.Decode(&req); err != nil {
		accountNewsError(w, service.ErrInvalidInput)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		accountNewsError(w, service.ErrInvalidInput)
		return
	}
	src, err := h.service.Create(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), &req)
	if err != nil {
		accountNewsError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, src)
}

func (h *AccountNewsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		accountNewsError(w, service.ErrInvalidInput)
		return
	}
	err := h.service.Delete(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), chi.URLParam(r, "sourceId"))
	if err != nil {
		accountNewsError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListAll handles GET /api/news-sources?limit=&cursor=: one page of the
// caller's common and account sources. Other or repeated parameters, a
// non-positive or non-numeric limit and a bad cursor are 400; limit is
// capped by the service.
func (h *AccountNewsHandler) ListAll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		accountNewsError(w, service.ErrInvalidInput)
		return
	}
	limit := 0
	for key, values := range query {
		if (key != "limit" && key != "cursor") || len(values) != 1 {
			accountNewsError(w, service.ErrInvalidInput)
			return
		}
	}
	if raw, ok := query["limit"]; ok {
		if limit, err = strconv.Atoi(raw[0]); err != nil || limit < 1 {
			accountNewsError(w, service.ErrInvalidInput)
			return
		}
	}
	resp, err := h.service.ListAll(r.Context(), middleware.GetUserID(r.Context()), query.Get("cursor"), limit)
	if err != nil {
		accountNewsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// Update handles PUT /api/accounts/{accountId}/news-sources/{sourceId}.
func (h *AccountNewsHandler) Update(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		accountNewsError(w, service.ErrInvalidInput)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var req model.UpdateAccountNewsSourceRequest
	if err := decoder.Decode(&req); err != nil {
		accountNewsError(w, service.ErrInvalidInput)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		accountNewsError(w, service.ErrInvalidInput)
		return
	}
	item, err := h.service.Update(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), chi.URLParam(r, "sourceId"), &req)
	if err != nil {
		accountNewsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// maxNewsCollectBody bounds the (required empty) collect request body.
const maxNewsCollectBody = 1024

// newsCollectLimited is the 429 and daily-window 409 body: the standard
// error plus nextAllowedAt.
type newsCollectLimited struct {
	model.ErrorResponse
	NextAllowedAt string `json:"nextAllowedAt"`
}

// Collect handles POST /api/accounts/{accountId}/news-sources/{sourceId}/collect:
// 202 with the request ID once the crawler has the request, 409
// COLLECT_DAILY_WINDOW (with nextAllowedAt) around the daily run, 409
// COLLECT_BUSY while a run is in progress, 429 with nextAllowedAt within an
// hour of the previous request, 503 when admission could not be checked or
// the request could not be handed over. The 409 checks are advisory; the
// starter may still refuse after a 202. It takes no query and an empty body.
func (h *AccountNewsHandler) Collect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		accountNewsError(w, service.ErrInvalidInput)
		return
	}
	if r.Body != nil {
		n, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxNewsCollectBody))
		if err != nil || n != 0 {
			accountNewsError(w, service.ErrInvalidInput)
			return
		}
	}
	resp, err := h.service.CollectNow(r.Context(), middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId"), chi.URLParam(r, "sourceId"))
	var limited *service.NewsCollectLimitError
	var window *service.NewsCollectWindowError
	switch {
	case errors.As(err, &window):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(newsCollectLimited{
			ErrorResponse: model.NewErrorResponse(model.ErrCodeCollectDailyWindow, "News collection is paused around the daily run"),
			NextAllowedAt: window.NextAllowedAt.UTC().Format(time.RFC3339),
		})
	case errors.Is(err, service.ErrNewsCollectBusy):
		writeError(w, http.StatusConflict, model.ErrCodeCollectBusy, "Another news collection is in progress; try again later")
	case errors.As(err, &limited):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(newsCollectLimited{
			ErrorResponse: model.NewErrorResponse(model.ErrCodeRateLimited, "News collection was requested within the last hour"),
			NextAllowedAt: limited.NextAllowedAt.UTC().Format(time.RFC3339),
		})
	case errors.Is(err, service.ErrNewsCollectUnavailable):
		writeError(w, http.StatusServiceUnavailable, model.ErrCodeInternalError, "News collection is unavailable; try again later")
	case err != nil:
		accountNewsError(w, err)
	default:
		writeJSON(w, http.StatusAccepted, resp)
	}
}
