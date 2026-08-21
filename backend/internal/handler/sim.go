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

// SimHandler handles the cost/sizing simulator endpoints (ADR-033).
// Kept separate from MeetingHandler (already 597+ lines) per the design
// doc's component-boundary decision.
type SimHandler struct {
	simService *service.SimService
}

// NewSimHandler creates a new simulation handler.
func NewSimHandler(simService *service.SimService) *SimHandler {
	return &SimHandler{simService: simService}
}

// ExtractRequirements handles POST /api/meetings/{meetingId}/sim/extract.
// Synchronous (~5s, Haiku): drafts a quantitative requirement set for the
// user to confirm/correct. Does not start a simulation run.
func (h *SimHandler) ExtractRequirements(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")
	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}

	run, err := h.simService.ExtractRequirements(ctx, userID, meetingID)
	if err != nil {
		writeSimError(w, err, "ExtractRequirements")
		return
	}
	writeJSON(w, http.StatusOK, model.ToSimRunResponse(run))
}

// CreateSimulation handles POST /api/meetings/{meetingId}/sim.
// Validates the user-confirmed requirements/options and hands off to
// ttobak-sim asynchronously; the frontend polls GetMeeting for status.
func (h *SimHandler) CreateSimulation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")
	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}

	var req model.CreateSimulationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	run, err := h.simService.CreateSimulation(ctx, userID, meetingID, req.Requirements, req.Options)
	if err != nil {
		writeSimError(w, err, "CreateSimulation")
		return
	}
	writeJSON(w, http.StatusAccepted, model.ToSimRunResponse(run))
}

func writeSimError(w http.ResponseWriter, err error, op string) {
	switch {
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, err.Error())
	default:
		log.Printf("%s failed: %v", op, err)
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to process simulation request")
	}
}
