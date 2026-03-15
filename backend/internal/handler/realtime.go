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
// This is now async - returns immediately after triggering ECS scale-up.
func (h *RealtimeHandler) StartRealtime(w http.ResponseWriter, r *http.Request) {
	// First check if already running
	running, wsURL, err := h.realtimeService.GetStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to check status: "+err.Error())
		return
	}

	if running {
		// Already running - return immediately with ready status
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"websocketUrl": wsURL,
			"status":       "ready",
		})
		return
	}

	// Trigger ECS scale-up (async, returns immediately)
	err = h.realtimeService.StartRealtimeAsync(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Failed to start realtime service: "+err.Error())
		return
	}

	// Return immediately with starting status - client should poll /status
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "starting",
	})
}

// StatusRealtime handles GET /api/realtime/status
// Response: {"websocketUrl": "ws://alb-dns/ws", "status": "ready"} or {"status": "starting"}
func (h *RealtimeHandler) StatusRealtime(w http.ResponseWriter, r *http.Request) {
	running, wsURL, err := h.realtimeService.GetStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Failed to check status: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if running {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"websocketUrl": wsURL,
			"status":       "ready",
		})
	} else {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "starting",
		})
	}
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
