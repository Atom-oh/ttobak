package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

const accountPeopleMaxBody = 4 * 1024

type accountPeopleAPI interface {
	ListPeople(ctx context.Context, userID, accountID, cursor string) (*model.AccountPeoplePage, error)
	ListMentionGroups(ctx context.Context, userID, accountID, cursor string) (*model.MentionGroupsPage, error)
	MeetingEdges(ctx context.Context, userID, accountID, meetingID string) (*model.MeetingPeopleEdges, error)
	Focus(ctx context.Context, userID, accountID, focus, cursor string) (*model.AccountFocusGraph, error)
	BindEdge(ctx context.Context, userID, accountID string, in model.PeopleEdgeInput) (*model.PeopleLink, error)
	UnbindEdge(ctx context.Context, userID, accountID string, k model.PeopleLinkKey) error
	ResolveMentionGroup(ctx context.Context, userID, accountID, hash string, in model.ResolveMentionInput) (*model.ResolveMentionResult, error)
	IgnoreMentionGroup(ctx context.Context, userID, accountID, hash string) error
	RequestContactsResolve(ctx context.Context, userID, accountID string) error
}

// AccountPeopleHandler serves the people panel, mention review and speaker
// binding (direct members only; never delegable).
type AccountPeopleHandler struct{ service accountPeopleAPI }

func NewAccountPeopleHandler(s accountPeopleAPI) *AccountPeopleHandler {
	return &AccountPeopleHandler{service: s}
}

func accountPeopleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Not found")
	case errors.Is(err, service.ErrAccountMerging):
		writeAccountMerging(w)
	case errors.Is(err, service.ErrConflict):
		writeError(w, http.StatusConflict, model.ErrCodeConflict, "Changed concurrently, retry")
	case errors.Is(err, service.ErrContactsUnavailable):
		writeError(w, http.StatusServiceUnavailable, "CONTACTS_UNAVAILABLE", "Contacts are temporarily unavailable")
	default:
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Internal server error")
	}
}

func peopleCaller(r *http.Request) (string, string) {
	return middleware.GetUserID(r.Context()), chi.URLParam(r, "accountId")
}

func peoplePage[T any](w http.ResponseWriter, r *http.Request, list func(context.Context, string, string, string) (T, error)) {
	w.Header().Set("Cache-Control", "no-store")
	if !onlyQueryKeys(r, "cursor") {
		accountPeopleError(w, service.ErrInvalidInput)
		return
	}
	user, account := peopleCaller(r)
	page, err := list(r.Context(), user, account, r.URL.Query().Get("cursor"))
	if err != nil {
		accountPeopleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *AccountPeopleHandler) List(w http.ResponseWriter, r *http.Request) {
	peoplePage(w, r, h.service.ListPeople)
}

func (h *AccountPeopleHandler) Mentions(w http.ResponseWriter, r *http.Request) {
	peoplePage(w, r, h.service.ListMentionGroups)
}

// mentionKey reads {mentionKey}; anything but 32 lowercase hex is 400.
func mentionKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := chi.URLParam(r, "mentionKey")
	if !service.ValidMentionKey(key) || r.URL.RawQuery != "" {
		accountPeopleError(w, service.ErrInvalidInput)
		return "", false
	}
	return key, true
}

func (h *AccountPeopleHandler) ResolveMention(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key, ok := mentionKey(w, r)
	if !ok {
		return
	}
	var in model.ResolveMentionInput
	if !decodeStrict(w, r, accountPeopleMaxBody, &in) {
		return
	}
	user, account := peopleCaller(r)
	out, err := h.service.ResolveMentionGroup(r.Context(), user, account, key, in)
	if err != nil {
		accountPeopleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *AccountPeopleHandler) IgnoreMention(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key, ok := mentionKey(w, r)
	if !ok {
		return
	}
	user, account := peopleCaller(r)
	if err := h.service.IgnoreMentionGroup(r.Context(), user, account, key); err != nil {
		accountPeopleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AccountPeopleHandler) Edges(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !onlyQueryKeys(r, "meetingId") || len(r.URL.Query()) != 1 {
		accountPeopleError(w, service.ErrInvalidInput)
		return
	}
	user, account := peopleCaller(r)
	out, err := h.service.MeetingEdges(r.Context(), user, account, r.URL.Query().Get("meetingId"))
	if err != nil {
		accountPeopleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Focus handles GET /api/accounts/{accountId}/people/focus?focus=&cursor=.
func (h *AccountPeopleHandler) Focus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query := r.URL.Query()
	if !onlyQueryKeys(r, "focus", "cursor") || len(query["focus"]) != 1 || len(query["cursor"]) > 1 {
		accountPeopleError(w, service.ErrInvalidInput)
		return
	}
	user, account := peopleCaller(r)
	out, err := h.service.Focus(r.Context(), user, account, query.Get("focus"), query.Get("cursor"))
	if err != nil {
		accountPeopleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *AccountPeopleHandler) Bind(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		accountPeopleError(w, service.ErrInvalidInput)
		return
	}
	var in model.PeopleEdgeInput
	if !decodeStrict(w, r, accountPeopleMaxBody, &in) {
		return
	}
	user, account := peopleCaller(r)
	link, err := h.service.BindEdge(r.Context(), user, account, in)
	if err != nil {
		accountPeopleError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, link)
}

func (h *AccountPeopleHandler) Unbind(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	keys := []string{"meetingId", "toKind", "toId", "role"}
	query := r.URL.Query()
	if !onlyQueryKeys(r, keys...) || len(query) != len(keys) {
		accountPeopleError(w, service.ErrInvalidInput)
		return
	}
	k := model.PeopleLinkKey{MeetingID: query.Get("meetingId"), ToKind: query.Get("toKind"), ToID: query.Get("toId"), Role: query.Get("role")}
	user, account := peopleCaller(r)
	if err := h.service.UnbindEdge(r.Context(), user, account, k); err != nil {
		accountPeopleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Resolve queues one account people resolve request (end of a bulk import).
func (h *AccountPeopleHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		accountPeopleError(w, service.ErrInvalidInput)
		return
	}
	user, account := peopleCaller(r)
	if err := h.service.RequestContactsResolve(r.Context(), user, account); err != nil {
		accountPeopleError(w, err)
		return
	}
	// A JSON body: clients parse every non-204 response.
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}
