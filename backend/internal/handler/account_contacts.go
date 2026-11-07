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

type accountContactsAPI interface {
	List(context.Context, string, string, int, string) (*model.AccountContactsPage, error)
	Mutate(context.Context, string, string, string, string, model.AccountContact) (*model.AccountContact, error)
	SpeakerTeamPage(context.Context, string, string, int, string) (*model.SpeakerTeam, error)
}
type AccountContactsHandler struct{ service accountContactsAPI }

func NewAccountContactsHandler(s accountContactsAPI) *AccountContactsHandler {
	return &AccountContactsHandler{service: s}
}

func contactError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, 400, model.ErrCodeBadRequest, "Invalid request")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, 403, model.ErrCodeForbidden, "Access denied")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, 404, model.ErrCodeNotFound, "Contact not found")
	case errors.Is(err, service.ErrContactConflict):
		writeError(w, 409, model.ErrCodeConflict, "Contact changed; reload and retry")
	default:
		writeError(w, 500, model.ErrCodeInternalError, "Internal server error")
	}
}
func (h *AccountContactsHandler) List(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		contactError(w, service.ErrInvalidInput)
		return
	}
	limit := 25
	if v, ok := values["limit"]; ok {
		if len(v) != 1 {
			contactError(w, service.ErrInvalidInput)
			return
		}
		n, err := strconv.Atoi(v[0])
		if err != nil || n < 1 || n > 25 {
			contactError(w, service.ErrInvalidInput)
			return
		}
		limit = n
	}
	if len(values["cursor"]) > 1 {
		contactError(w, service.ErrInvalidInput)
		return
	}
	page, err := h.service.List(r.Context(), chi.URLParam(r, "accountId"), middleware.GetUserID(r.Context()), limit, values.Get("cursor"))
	if err != nil {
		contactError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
func (h *AccountContactsHandler) SpeakerTeam(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(values["limit"]) > 1 || len(values["cursor"]) > 1 {
		contactError(w, service.ErrInvalidInput)
		return
	}
	limit := 25
	if v, ok := values["limit"]; ok {
		limit, err = strconv.Atoi(v[0])
		if err != nil || limit < 1 || limit > 25 {
			contactError(w, service.ErrInvalidInput)
			return
		}
	}
	team, err := h.service.SpeakerTeamPage(r.Context(), chi.URLParam(r, "accountId"), middleware.GetUserID(r.Context()), limit, values.Get("cursor"))
	if err != nil {
		contactError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, team)
}
func (h *AccountContactsHandler) mutate(w http.ResponseWriter, r *http.Request, op string) {
	w.Header().Set("Cache-Control", "no-store")
	var c model.AccountContact
	if op == "delete" {
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(values) != 1 || len(values["revision"]) != 1 {
			contactError(w, service.ErrInvalidInput)
			return
		}
		c.Revision = values.Get("revision")
	}
	if op != "delete" {
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&c); err != nil {
			contactError(w, service.ErrInvalidInput)
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			contactError(w, service.ErrInvalidInput)
			return
		}
	}
	out, err := h.service.Mutate(r.Context(), chi.URLParam(r, "accountId"), middleware.GetUserID(r.Context()), op, chi.URLParam(r, "contactId"), c)
	if err != nil {
		contactError(w, err)
		return
	}
	if op == "delete" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	status := http.StatusOK
	if op == "create" {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
}
func (h *AccountContactsHandler) Create(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, "create")
}
func (h *AccountContactsHandler) Update(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, "update")
}
func (h *AccountContactsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, "delete")
}
