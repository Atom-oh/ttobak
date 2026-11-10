package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type accountEventsAPI interface {
	List(context.Context, string, string, service.EventListOptions) (*model.AccountEventsPage, error)
	Get(context.Context, string, string, string) (*model.AccountEvent, error)
	Mutate(context.Context, string, string, string, string, service.AccountEventInput) (*model.AccountEvent, error)
}

type AccountEventsHandler struct{ service accountEventsAPI }

func NewAccountEventsHandler(s accountEventsAPI) *AccountEventsHandler {
	return &AccountEventsHandler{service: s}
}

func eventError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, 400, model.ErrCodeBadRequest, "Invalid request")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, 403, model.ErrCodeForbidden, "Access denied")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, 404, model.ErrCodeNotFound, "Event not found")
	case errors.Is(err, service.ErrAccountMerging):
		writeAccountMerging(w)
	case errors.Is(err, service.ErrEventConflict):
		writeError(w, 409, model.ErrCodeConflict, "Event changed; reload and retry")
	case errors.Is(err, service.ErrEventsUnavailable):
		writeError(w, 503, "EVENTS_UNAVAILABLE", "Events are temporarily unavailable")
	default:
		writeError(w, 500, model.ErrCodeInternalError, "Internal server error")
	}
}

func singleQuery(values url.Values, allowed ...string) bool {
	allow := map[string]bool{}
	for _, k := range allowed {
		allow[k] = true
	}
	for k, v := range values {
		if !allow[k] || len(v) != 1 {
			return false
		}
	}
	return true
}

func (h *AccountEventsHandler) List(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || !singleQuery(values, "from", "to", "order", "limit", "cursor") {
		eventError(w, service.ErrInvalidInput)
		return
	}
	o := service.EventListOptions{From: values.Get("from"), To: values.Get("to"), Limit: 25, Cursor: values.Get("cursor")}
	switch values.Get("order") {
	case "", "asc":
	case "desc":
		o.Descending = true
	default:
		eventError(w, service.ErrInvalidInput)
		return
	}
	if v := values.Get("limit"); v != "" {
		if o.Limit, err = strconv.Atoi(v); err != nil || o.Limit < 1 || o.Limit > 25 {
			eventError(w, service.ErrInvalidInput)
			return
		}
	}
	page, err := h.service.List(r.Context(), chi.URLParam(r, "accountId"), middleware.GetUserID(r.Context()), o)
	if err != nil {
		eventError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *AccountEventsHandler) Get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		eventError(w, service.ErrInvalidInput)
		return
	}
	e, err := h.service.Get(r.Context(), chi.URLParam(r, "accountId"), middleware.GetUserID(r.Context()), chi.URLParam(r, "eventId"))
	if err != nil {
		eventError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (h *AccountEventsHandler) mutate(w http.ResponseWriter, r *http.Request, op string) {
	w.Header().Set("Cache-Control", "no-store")
	var in service.AccountEventInput
	if op == "delete" {
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(values) != 1 || len(values["revision"]) != 1 {
			eventError(w, service.ErrInvalidInput)
			return
		}
		in.Revision = values.Get("revision")
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&in); err != nil {
			eventError(w, service.ErrInvalidInput)
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			eventError(w, service.ErrInvalidInput)
			return
		}
	}
	out, err := h.service.Mutate(r.Context(), chi.URLParam(r, "accountId"), middleware.GetUserID(r.Context()), op, chi.URLParam(r, "eventId"), in)
	if err != nil {
		eventError(w, err)
		return
	}
	switch op {
	case "delete":
		w.WriteHeader(http.StatusNoContent)
	case "create":
		writeJSON(w, http.StatusCreated, out)
	default:
		writeJSON(w, http.StatusOK, out)
	}
}

func (h *AccountEventsHandler) Create(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, "create")
}
func (h *AccountEventsHandler) Update(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, "update")
}
func (h *AccountEventsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, "delete")
}
