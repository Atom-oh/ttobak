package handler

import (
	"encoding/json"
	"net/http"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type TranslateHandler struct {
	translateService *service.TranslateService
}

func NewTranslateHandler(translateService *service.TranslateService) *TranslateHandler {
	return &TranslateHandler{translateService: translateService}
}

// Translate handles POST /api/translate
func (h *TranslateHandler) Translate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text       string `json:"text"`
		SourceLang string `json:"sourceLang"`
		TargetLang string `json:"targetLang"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if req.Text == "" || req.TargetLang == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "text and targetLang are required")
		return
	}
	if req.SourceLang == "" {
		req.SourceLang = "auto"
	}

	translated, err := h.translateService.Translate(r.Context(), req.Text, req.SourceLang, req.TargetLang)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"translatedText": translated,
		"sourceLang":     req.SourceLang,
		"targetLang":     req.TargetLang,
	})
}
