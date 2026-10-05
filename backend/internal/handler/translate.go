package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

const (
	// Matches the frontend's 9,000-byte segment cap and stays under Amazon
	// Translate's 10,000-byte document limit.
	translateMaxTextBytes    = 9000
	translateMaxContextItems = 2
	translateMaxContextBytes = 2000
	translateMaxBodyBytes    = 32 * 1024
)

var (
	translateSourceLangs = map[string]bool{"auto": true, "ko": true, "en": true, "ja": true, "zh": true, "es": true, "fr": true, "de": true}
	translateTargetLangs = map[string]bool{"ko": true, "en": true, "ja": true, "zh": true, "es": true, "fr": true, "de": true}
)

type segmentTranslator interface {
	TranslateSegment(context.Context, service.TranslateRequest) (service.TranslateResult, error)
}

type TranslateHandler struct {
	translateService segmentTranslator
}

func NewTranslateHandler(translateService segmentTranslator) *TranslateHandler {
	return &TranslateHandler{translateService: translateService}
}

type translateRequest struct {
	Text       string   `json:"text"`
	SourceLang string   `json:"sourceLang"`
	TargetLang string   `json:"targetLang"`
	Context    []string `json:"context"`
	Quality    string   `json:"quality"`
}

// Translate handles POST /api/translate.
//
// quality "fast" (default) uses Amazon Translate. "high" asks the interpreter
// model and falls back to Amazon Translate; the response engine tells which
// one produced the text.
func (h *TranslateHandler) Translate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, translateMaxBodyBytes)
	var req translateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if strings.TrimSpace(req.Text) == "" || req.TargetLang == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "text and targetLang are required")
		return
	}
	if len(req.Text) > translateMaxTextBytes {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "text is too long")
		return
	}
	if req.SourceLang == "" {
		req.SourceLang = "auto"
	}
	if !translateSourceLangs[req.SourceLang] || !translateTargetLangs[req.TargetLang] {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "unsupported language")
		return
	}
	if req.SourceLang == req.TargetLang {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "sourceLang and targetLang must differ")
		return
	}
	switch req.Quality {
	case "":
		req.Quality = service.TranslateQualityFast
	case service.TranslateQualityFast, service.TranslateQualityHigh:
	default:
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "unsupported quality")
		return
	}
	if len(req.Context) > translateMaxContextItems {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "too many context segments")
		return
	}
	contextBytes := 0
	for _, item := range req.Context {
		contextBytes += len(item)
	}
	if contextBytes > translateMaxContextBytes {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "context is too long")
		return
	}

	result, err := h.translateService.TranslateSegment(r.Context(), service.TranslateRequest{
		Text: req.Text, SourceLang: req.SourceLang, TargetLang: req.TargetLang,
		Context: req.Context, Quality: req.Quality,
	})
	if err != nil {
		// Segment text is meeting content: log the failure, never the input.
		log.Printf("translate failed: quality=%s source=%s target=%s err=%v", req.Quality, req.SourceLang, req.TargetLang, err)
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, "Translation failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"translatedText": result.Text,
		"sourceLang":     req.SourceLang,
		"targetLang":     req.TargetLang,
		"quality":        req.Quality,
		"engine":         result.Engine,
	})
}
