package proxy

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/codex2api/database"
	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const requestTraceIDHeader = "X-Request-ID"

type requestTrace struct {
	RequestID      string
	Endpoint       string
	Model          string
	EffectiveModel string
	Stream         bool
	StartedAt      time.Time
}

type requestTraceFields struct {
	AccountID      int64
	Attempt        int
	StatusCode     int
	ErrorKind      string
	Message        string
	EffectiveModel string
}

func newRequestTrace(c *gin.Context, endpoint, model string, stream bool) *requestTrace {
	requestID := strings.TrimSpace(c.GetHeader(requestTraceIDHeader))
	if requestID == "" {
		requestID = strings.TrimSpace(c.GetHeader("X-Codex-Request-ID"))
	}
	if requestID == "" {
		requestID = uuid.NewString()
	}
	if len(requestID) > 64 {
		requestID = requestID[:64]
	}
	c.Header(requestTraceIDHeader, requestID)
	return &requestTrace{
		RequestID: strings.TrimSpace(requestID),
		Endpoint:  strings.TrimSpace(endpoint),
		Model:     strings.TrimSpace(model),
		Stream:    stream,
		StartedAt: time.Now(),
	}
}

func (h *Handler) traceRequestEvent(c *gin.Context, trace *requestTrace, stage string, fields requestTraceFields) {
	if h == nil || h.db == nil || trace == nil {
		return
	}
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return
	}
	input := &database.RequestTraceEventInput{
		RequestID:      trace.RequestID,
		APIKeyID:       requestAPIKeyID(c),
		AccountID:      fields.AccountID,
		Endpoint:       trace.Endpoint,
		Model:          trace.Model,
		EffectiveModel: strings.TrimSpace(firstNonEmpty(fields.EffectiveModel, trace.EffectiveModel)),
		Stream:         trace.Stream,
		Stage:          stage,
		Attempt:        fields.Attempt,
		ElapsedMs:      int(time.Since(trace.StartedAt).Milliseconds()),
		StatusCode:     fields.StatusCode,
		ErrorKind:      strings.TrimSpace(fields.ErrorKind),
		Message:        traceMessage(fields.Message),
	}
	if v, exists := c.Get(contextAPIKeyName); exists && v != nil {
		if name, ok := v.(string); ok {
			input.APIKeyName = security.SafeTruncate(security.SanitizeLog(name), 255)
		}
	}
	if v, exists := c.Get(contextAPIKeyMasked); exists && v != nil {
		if masked, ok := v.(string); ok {
			input.APIKeyMasked = security.SafeTruncate(security.SanitizeLog(masked), 64)
		}
	}

	if err := h.db.InsertRequestTraceEvent(context.Background(), input); err != nil {
		log.Printf("写入请求诊断事件失败 request_id=%s stage=%s err=%v", trace.RequestID, stage, err)
	}
}

func (h *Handler) traceRequestFallback(c *gin.Context, trace *requestTrace, terminal *bool) {
	if terminal == nil || *terminal || trace == nil {
		return
	}
	statusCode := c.Writer.Status()
	if statusCode <= 0 || statusCode == http.StatusOK {
		h.traceRequestEvent(c, trace, "request_returned", requestTraceFields{
			StatusCode: statusCode,
			Message:    "handler returned before an explicit terminal trace event",
		})
		return
	}
	h.traceRequestEvent(c, trace, "request_failed", requestTraceFields{
		StatusCode: statusCode,
		ErrorKind:  "handler",
		Message:    "handler returned with error before upstream completion",
	})
}

func traceMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	return security.SafeTruncate(security.SanitizeLog(message), 600)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
