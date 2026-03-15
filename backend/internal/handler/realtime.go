package handler

import (
	"encoding/json"
	"net/http"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// RealtimeHandler handles realtime STT control requests
type RealtimeHandler struct {
	realtimeService *service.RealtimeService
}

// NewRealtimeHandler creates a new realtime handler
func NewRealtimeHandler(realtimeService *service.RealtimeService) *RealtimeHandler {
	return &RealtimeHandler{realtimeService: realtimeService}
}

// StartRealtime handles POST /api/realtime/start
// Response: {"websocketUrl": "ws://alb-dns/ws", "status": "starting"|"ready"}
func (h *RealtimeHandler) StartRealtime(w http.ResponseWriter, r *http.Request) {
	// First check if already running
	running, wsURL, err := h.realtimeService.GetStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to check status: "+err.Error())
		return
	}

	if running {
		// Already running - return immediately
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"websocketUrl": wsURL,
			"status":       "ready",
		})
		return
	}

	// Start the ECS service and wait for it
	wsURL, err = h.realtimeService.StartRealtime(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Failed to start realtime service: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"websocketUrl": wsURL,
		"status":       "ready",
	})
}

// StopRealtime handles POST /api/realtime/stop
// Response: {"status": "stopped"}
func (h *RealtimeHandler) StopRealtime(w http.ResponseWriter, r *http.Request) {
	err := h.realtimeService.StopRealtime(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to stop realtime service: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "stopped",
	})
}
