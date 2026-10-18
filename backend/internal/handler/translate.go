package handler

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ttobak/backend/internal/middleware"
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

	// Per-user hourly counters. Every accepted request counts toward the
	// total; "high" requests also count toward the interpreter budget.
	translateRateBucket     = "translate"
	translateHighRateBucket = "translate-high"
	translateRateWindow     = time.Hour

	DefaultTranslateHourlyLimit     = 3000
	DefaultTranslateHighHourlyLimit = 1500
)

var (
	translateSourceLangs = map[string]bool{"auto": true, "ko": true, "en": true, "ja": true, "zh": true, "es": true, "fr": true, "de": true}
	translateTargetLangs = map[string]bool{"ko": true, "en": true, "ja": true, "zh": true, "es": true, "fr": true, "de": true}
)

type segmentTranslator interface {
	TranslateSegment(context.Context, service.TranslateRequest) (service.TranslateResult, error)
}

type translateLimiter interface {
	Exceeds(ctx context.Context, userID, bucket string, window time.Duration, n, limit int64) (bool, service.WindowUsage)
}

// TranslateLimits are hourly per-user request limits; 0 disables one.
type TranslateLimits struct {
	Hourly     int64
	HighHourly int64
}

type TranslateHandler struct {
	translateService segmentTranslator
	limiter          translateLimiter
	limits           TranslateLimits
	now              func() time.Time
}

func NewTranslateHandler(translateService segmentTranslator) *TranslateHandler {
	return &TranslateHandler{translateService: translateService, now: time.Now}
}

// SetRateLimits enables the cost guard. It is an abuse brake, not an access
// boundary: the limiter fails open when its storage is unavailable.
func (h *TranslateHandler) SetRateLimits(limiter translateLimiter, limits TranslateLimits) {
	h.limiter = limiter
	h.limits = limits
}

// translateMeter is the per-request metering line. It never carries segment
// text or the user ID.
type translateMeter struct {
	Event          string `json:"evt"`
	Quality        string `json:"q"`
	Engine         string `json:"engine"`
	Source         string `json:"src"`
	Target         string `json:"tgt"`
	InBytes        int    `json:"inBytes"`
	OutBytes       int    `json:"outBytes"`
	Millis         int64  `json:"ms"`
	Glossary       bool   `json:"glossary"`
	Limited        bool   `json:"limited"`
	FallbackReason string `json:"fallbackReason"`
}

func logTranslateMeter(m translateMeter) {
	m.Event = "translate"
	line, err := json.Marshal(m)
	if err != nil {
		return
	}
	log.Print(string(line))
}

func retryAfterSeconds(now, windowEnd time.Time) string {
	seconds := int64(math.Ceil(windowEnd.Sub(now).Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	return strconv.FormatInt(seconds, 10)
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

	requestedQuality := req.Quality
	limited := false
	if userID := middleware.GetUserID(r.Context()); h.limiter != nil && userID != "" {
		over, usage := h.limiter.Exceeds(r.Context(), userID, translateRateBucket, translateRateWindow, 1, h.limits.Hourly)
		if over {
			log.Printf(`{"evt":"translate_denied","q":%q,"src":%q,"tgt":%q}`, requestedQuality, req.SourceLang, req.TargetLang)
			w.Header().Set("Retry-After", retryAfterSeconds(h.now(), usage.WindowEnd))
			writeError(w, http.StatusTooManyRequests, model.ErrCodeRateLimited, "Translation limit reached; try again later")
			return
		}
		if req.Quality == service.TranslateQualityHigh {
			if over, _ := h.limiter.Exceeds(r.Context(), userID, translateHighRateBucket, translateRateWindow, 1, h.limits.HighHourly); over {
				req.Quality = service.TranslateQualityFast
				limited = true
			}
		}
	}

	started := h.now()
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

	logTranslateMeter(translateMeter{
		Quality: requestedQuality, Engine: result.Engine, Source: req.SourceLang, Target: req.TargetLang,
		InBytes: len(req.Text), OutBytes: len(result.Text), Millis: h.now().Sub(started).Milliseconds(),
		Limited: limited, FallbackReason: result.FallbackReason,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"translatedText": result.Text,
		"sourceLang":     req.SourceLang,
		"targetLang":     req.TargetLang,
		"quality":        req.Quality,
		"engine":         result.Engine,
		"limited":        limited,
	})
}
