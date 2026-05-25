package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/codex2api/database"
	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// ==================== Anthropic 错误格式 ====================

// sendAnthropicError 发送 Anthropic 格式的错误响应
func sendAnthropicError(c *gin.Context, statusCode int, errType, message string) {
	c.JSON(statusCode, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

// sendAnthropicStreamError 在流式模式中发送错误事件
func sendAnthropicStreamError(c *gin.Context, errType, message string) {
	_, _ = fmt.Fprint(c.Writer, anthropicStreamErrorSSE(errType, message))
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func anthropicStreamErrorSSE(errType, message string) string {
	payload, _ := json.Marshal(gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
	return fmt.Sprintf("event: error\ndata: %s\n\n", payload)
}

func anthropicStreamErrorForResponseFailed(payload []byte) string {
	outcome := classifyResponseFailedOutcome(payload)
	return anthropicStreamErrorSSE(mapHTTPStatusToAnthropicError(outcome.logStatusCode), outcome.failureMessage)
}

// mapHTTPStatusToAnthropicError 将 HTTP 状态码映射为 Anthropic 错误类型
func mapHTTPStatusToAnthropicError(statusCode int) string {
	switch {
	case statusCode == 400:
		return "invalid_request_error"
	case statusCode == 401:
		return "authentication_error"
	case statusCode == 403:
		return "permission_error"
	case statusCode == 404:
		return "not_found_error"
	case statusCode == 429:
		return "rate_limit_error"
	case statusCode == 529:
		return "overloaded_error"
	case statusCode >= 500:
		return "api_error"
	default:
		return "api_error"
	}
}

// ==================== /v1/messages Handler ====================

// Messages 处理 /v1/messages 请求（Anthropic Messages API → Codex Responses）
func (h *Handler) Messages(c *gin.Context) {
	// 1. 读取请求体
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		sendAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	trace := newRequestTrace(c, "/v1/messages", gjson.GetBytes(rawBody, "model").String(), gjson.GetBytes(rawBody, "stream").Bool())
	traceTerminal := false
	defer h.traceRequestFallback(c, trace, &traceTerminal)
	h.traceRequestEvent(c, trace, "request_start", requestTraceFields{
		Message: fmt.Sprintf("body_bytes=%d stream=%t", len(rawBody), trace.Stream),
	})

	if len(rawBody) == 0 {
		sendAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	// 验证 JSON
	if !gjson.ValidBytes(rawBody) {
		sendAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Invalid JSON in request body")
		return
	}

	// 检查请求体大小
	if len(rawBody) > security.MaxRequestBodySize {
		sendAnthropicError(c, http.StatusRequestEntityTooLarge, "invalid_request_error", "Request body too large")
		return
	}

	// 基本验证
	model := gjson.GetBytes(rawBody, "model").String()
	if model == "" {
		sendAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	if !gjson.GetBytes(rawBody, "messages").Exists() {
		sendAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "messages is required")
		return
	}
	if h.inspectPromptFilterAnthropic(c, rawBody, "/v1/messages", model) {
		return
	}

	isStream := gjson.GetBytes(rawBody, "stream").Bool()

	// 2. 翻译请求: Anthropic → Codex
	modelMappingJSON := h.store.GetModelMapping()
	codexBody, originalModel, err := TranslateAnthropicToCodexWithModels(rawBody, modelMappingJSON, h.supportedModelIDs(c.Request.Context()))
	if err != nil {
		sendAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Request translation failed: "+err.Error())
		return
	}
	effectiveModel := effectiveRequestModel(codexBody, model)
	trace.EffectiveModel = effectiveModel
	h.traceRequestEvent(c, trace, "request_validated", requestTraceFields{EffectiveModel: effectiveModel})
	if isImageOnlyModel(effectiveModel) {
		sendAnthropicError(c, http.StatusServiceUnavailable, "overloaded_error", fmt.Sprintf("model %s is only supported on /v1/images/generations and /v1/images/edits", effectiveModel))
		return
	}
	if h.enforceAPIKeyLimitsAndReply(c, effectiveModel) {
		return
	}
	accountFilter := accountFilterForModel(effectiveModel)
	accountFilter = h.withModelCooldownFilter(effectiveModel, accountFilter)

	// 提取 reasoning effort（从翻译后的 codex body 中）
	reasoningEffort := extractReasoningEffort(codexBody)
	sessionID := ResolveSessionID(c.Request.Header, codexBody)
	apiKeyID := requestAPIKeyID(c)
	affinityKey := sessionAffinityKey(sessionID, apiKeyID)

	// 3. 带重试的上游请求
	maxRetries := h.getMaxRetries()
	maxRateLimitRetries := h.getMaxRateLimitRetries()
	generalRetries := 0
	rateLimitRetries := 0
	var lastStatusCode int
	var lastBody []byte
	excludeAccounts := make(map[int64]bool)

	for attempt := 0; ; attempt++ {
		h.traceRequestEvent(c, trace, "attempt_start", requestTraceFields{
			Attempt:        attempt + 1,
			EffectiveModel: effectiveModel,
		})
		account, stickyProxyURL := h.nextAccountForSessionWithFilter(affinityKey, apiKeyID, excludeAccounts, accountFilter)
		if account == nil {
			h.traceRequestEvent(c, trace, "queue_wait_start", requestTraceFields{
				Attempt:        attempt + 1,
				EffectiveModel: effectiveModel,
				Message:        "no account immediately available; waiting up to 30s",
			})
			account, stickyProxyURL = h.store.WaitForSessionAvailableWithFilter(c.Request.Context(), affinityKey, 30*time.Second, apiKeyID, excludeAccounts, accountFilter)
			if account == nil {
				if lastStatusCode == http.StatusTooManyRequests && len(lastBody) > 0 {
					traceTerminal = true
					h.traceRequestEvent(c, trace, "request_failed", requestTraceFields{
						Attempt:        attempt + 1,
						StatusCode:     http.StatusTooManyRequests,
						ErrorKind:      "rate_limit",
						EffectiveModel: effectiveModel,
						Message:        "no account became available after upstream rate limit retries",
					})
					sendAnthropicError(c, http.StatusTooManyRequests, "rate_limit_error", "All accounts rate limited")
					return
				}
				traceTerminal = true
				h.traceRequestEvent(c, trace, "request_failed", requestTraceFields{
					Attempt:        attempt + 1,
					StatusCode:     http.StatusServiceUnavailable,
					ErrorKind:      ErrorCodeNoAvailableAccount,
					EffectiveModel: effectiveModel,
					Message:        "no account became available after queue wait",
				})
				sendAnthropicError(c, http.StatusServiceUnavailable, "overloaded_error", noAvailableAnthropicAccountMessage(effectiveModel))
				return
			}
			h.traceRequestEvent(c, trace, "queue_wait_end", requestTraceFields{
				AccountID:      account.ID(),
				Attempt:        attempt + 1,
				EffectiveModel: effectiveModel,
				Message:        "account became available",
			})
		}

		start := time.Now()
		proxyURL := h.resolveProxyForAttempt(account, stickyProxyURL)
		h.store.BindSessionAffinity(affinityKey, account, proxyURL)
		useWebsocket := h.shouldUseWebsocketForHTTP()
		h.traceRequestEvent(c, trace, "account_selected", requestTraceFields{
			AccountID:      account.ID(),
			Attempt:        attempt + 1,
			EffectiveModel: effectiveModel,
			Message:        fmt.Sprintf("transport_websocket=%t", useWebsocket),
		})

		apiKey := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		apiKey = strings.TrimSpace(apiKey)
		// 兼容 Anthropic 客户端多种认证方式
		if apiKey == "" {
			for _, hdr := range []string{"x-api-key", "anthropic-auth-token"} {
				if v := strings.TrimSpace(c.GetHeader(hdr)); v != "" {
					apiKey = v
					break
				}
			}
		}

		deviceCfg := h.deviceCfg
		if deviceCfg == nil {
			deviceCfg = &DeviceProfileConfig{StabilizeDeviceProfile: false}
		}

		downstreamHeaders := c.Request.Header.Clone()
		upstreamSessionID := IsolateCodexSessionID(apiKeyID, sessionID)
		h.traceRequestEvent(c, trace, "upstream_start", requestTraceFields{
			AccountID:      account.ID(),
			Attempt:        attempt + 1,
			EffectiveModel: effectiveModel,
			Message:        fmt.Sprintf("sending request via codex account websocket=%t", useWebsocket),
		})
		resp, reqErr := ExecuteRequest(c.Request.Context(), account, codexBody, upstreamSessionID, proxyURL, apiKey, deviceCfg, downstreamHeaders, useWebsocket)
		durationMs := int(time.Since(start).Milliseconds())

		if reqErr != nil {
			h.traceRequestEvent(c, trace, "upstream_error", requestTraceFields{
				AccountID:      account.ID(),
				Attempt:        attempt + 1,
				StatusCode:     http.StatusBadGateway,
				ErrorKind:      classifyTransportFailure(reqErr),
				EffectiveModel: effectiveModel,
				Message:        reqErr.Error(),
			})
			if kind := classifyTransportFailure(reqErr); kind != "" {
				h.store.ReportRequestFailure(account, kind, time.Duration(durationMs)*time.Millisecond)
			}
			h.store.Release(account)
			h.store.UnbindSessionAffinity(affinityKey, account.ID())
			excludeAccounts[account.ID()] = true

			if !IsRetryableError(reqErr) && classifyTransportFailure(reqErr) == "" {
				traceTerminal = true
				h.traceRequestEvent(c, trace, "request_failed", requestTraceFields{
					AccountID:      account.ID(),
					Attempt:        attempt + 1,
					StatusCode:     http.StatusBadGateway,
					ErrorKind:      "request_error",
					EffectiveModel: effectiveModel,
					Message:        reqErr.Error(),
				})
				sendAnthropicError(c, http.StatusBadGateway, "api_error", "Upstream request failed")
				return
			}

			log.Printf("上游请求失败 (attempt %d, /v1/messages): %v", attempt+1, reqErr)
			if shouldRetryRequestError(reqErr, &generalRetries, maxRetries) {
				h.traceRequestEvent(c, trace, "retry_scheduled", requestTraceFields{
					AccountID:      account.ID(),
					Attempt:        attempt + 1,
					ErrorKind:      classifyTransportFailure(reqErr),
					EffectiveModel: effectiveModel,
					Message:        "retrying after request error",
				})
				continue
			}
			traceTerminal = true
			h.traceRequestEvent(c, trace, "request_failed", requestTraceFields{
				AccountID:      account.ID(),
				Attempt:        attempt + 1,
				StatusCode:     http.StatusBadGateway,
				ErrorKind:      classifyTransportFailure(reqErr),
				EffectiveModel: effectiveModel,
				Message:        reqErr.Error(),
			})
			sendAnthropicError(c, http.StatusBadGateway, "api_error", "Upstream request failed")
			return
		}
		h.traceRequestEvent(c, trace, "upstream_headers", requestTraceFields{
			AccountID:      account.ID(),
			Attempt:        attempt + 1,
			StatusCode:     resp.StatusCode,
			EffectiveModel: effectiveModel,
			Message:        fmt.Sprintf("upstream responded in %dms", durationMs),
		})

		if resp.StatusCode != http.StatusOK {
			if kind := classifyHTTPFailure(resp.StatusCode); kind != "" {
				h.store.ReportRequestFailure(account, kind, time.Duration(durationMs)*time.Millisecond)
			}
			if usagePct, ok := parseCodexUsageHeaders(resp, account); ok {
				h.store.PersistUsageSnapshot(account, usagePct)
			}
			errBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			h.store.Release(account)
			h.store.UnbindSessionAffinity(affinityKey, account.ID())
			excludeAccounts[account.ID()] = true

			log.Printf("上游返回错误 (attempt %d, status %d, /v1/messages): %s", attempt+1, resp.StatusCode, string(errBody))
			logUpstreamError("/v1/messages", resp.StatusCode, model, account.ID(), errBody)
			h.logUpstreamCyberPolicy(c, "/v1/messages", model, errBody)
			decision := h.applyCooldownForModel(account, resp.StatusCode, errBody, resp, effectiveModel)
			shouldRetry := shouldRetryHTTPStatus(resp.StatusCode, &generalRetries, &rateLimitRetries, maxRetries, maxRateLimitRetries)
			errorKind := upstreamErrorKind(resp.StatusCode, errBody, decision)
			h.traceRequestEvent(c, trace, "upstream_error", requestTraceFields{
				AccountID:      account.ID(),
				Attempt:        attempt + 1,
				StatusCode:     resp.StatusCode,
				ErrorKind:      errorKind,
				EffectiveModel: effectiveModel,
				Message:        usageLogErrorMessage(resp.StatusCode, errBody),
			})
			h.logUsageForRequest(c, &database.UsageLogInput{
				AccountID:         account.ID(),
				Endpoint:          "/v1/messages",
				Model:             model,
				EffectiveModel:    effectiveModel,
				StatusCode:        resp.StatusCode,
				DurationMs:        durationMs,
				ReasoningEffort:   reasoningEffort,
				InboundEndpoint:   "/v1/messages",
				UpstreamEndpoint:  "/v1/responses",
				Stream:            isStream,
				IsRetryAttempt:    shouldRetry,
				AttemptIndex:      attempt + 1,
				UpstreamErrorKind: errorKind,
				ErrorMessage:      usageLogErrorMessage(resp.StatusCode, errBody),
			})

			if shouldRetry {
				lastStatusCode = resp.StatusCode
				lastBody = errBody
				h.traceRequestEvent(c, trace, "retry_scheduled", requestTraceFields{
					AccountID:      account.ID(),
					Attempt:        attempt + 1,
					StatusCode:     resp.StatusCode,
					ErrorKind:      errorKind,
					EffectiveModel: effectiveModel,
					Message:        "retrying after upstream HTTP error",
				})
				continue
			}

			// 最终错误：用 Anthropic 格式返回
			errType := mapHTTPStatusToAnthropicError(resp.StatusCode)
			msg := gjson.GetBytes(errBody, "error.message").String()
			if msg == "" {
				msg = fmt.Sprintf("Upstream returned status %d", resp.StatusCode)
			}
			traceTerminal = true
			h.traceRequestEvent(c, trace, "request_failed", requestTraceFields{
				AccountID:      account.ID(),
				Attempt:        attempt + 1,
				StatusCode:     resp.StatusCode,
				ErrorKind:      errorKind,
				EffectiveModel: effectiveModel,
				Message:        msg,
			})
			sendAnthropicError(c, resp.StatusCode, errType, msg)
			return
		}

		// ========== 成功路径 ==========
		account.Mu().RLock()
		c.Set("x-account-email", account.Email)
		account.Mu().RUnlock()
		c.Set("x-account-proxy", proxyURL)
		c.Set("x-model", effectiveModel)
		c.Set("x-reasoning-effort", reasoningEffort)

		var firstTokenMs int
		var usage *UsageInfo
		ttftRecorded := false
		gotTerminal := false
		deltaCharCount := 0
		var readErr error
		var writeErr error
		wroteAnyBody := false
		var terminalFailurePayload []byte
		firstSSETraced := false
		firstContentTraced := false

		if isStream {
			// 流式响应：逐事件翻译为 Anthropic SSE
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
			c.Header("Connection", "keep-alive")
			c.Header("X-Accel-Buffering", "no")

			flusher, ok := c.Writer.(http.Flusher)
			if !ok {
				sendAnthropicError(c, http.StatusInternalServerError, "api_error", "Streaming not supported")
				resp.Body.Close()
				h.store.Release(account)
				return
			}

			translator := newAnthropicStreamTranslator(originalModel)
			streamWriter := newStreamFlushWriter(c.Writer, flusher)
			stopKeepalive := startStreamKeepalive(c.Request.Context(), streamWriter)

			readErr = ReadSSEStreamWithIdleTimeout(resp.Body, currentStreamIdleTimeout(), func(data []byte) bool {
				parsed := gjson.ParseBytes(data)
				eventType := parsed.Get("type").String()
				if !firstSSETraced {
					firstSSETraced = true
					h.traceRequestEvent(c, trace, "first_sse", requestTraceFields{
						AccountID:      account.ID(),
						Attempt:        attempt + 1,
						EffectiveModel: effectiveModel,
						Message:        eventType,
					})
				}

				// TTFT 跟踪
				if !ttftRecorded && isFirstTokenEvent(eventType) {
					firstTokenMs = int(time.Since(start).Milliseconds())
					ttftRecorded = true
				}
				if !firstContentTraced && isFirstTokenEvent(eventType) {
					firstContentTraced = true
					h.traceRequestEvent(c, trace, "first_content", requestTraceFields{
						AccountID:      account.ID(),
						Attempt:        attempt + 1,
						EffectiveModel: effectiveModel,
						Message:        eventType,
					})
				}

				// 累计 delta 字符数
				if eventType == "response.output_text.delta" || eventType == "response.function_call_arguments.delta" {
					deltaCharCount += len(parsed.Get("delta").String())
				}

				// 提取 usage
				if eventType == "response.completed" {
					usage = extractUsageFromResult(parsed.Get("response.usage"))
					gotTerminal = true
				}
				if eventType == "response.failed" {
					terminalFailurePayload = append([]byte(nil), data...)
					gotTerminal = true
					statusCode := responseFailedStatusCode(data)
					h.traceRequestEvent(c, trace, "response_failed", requestTraceFields{
						AccountID:      account.ID(),
						Attempt:        attempt + 1,
						StatusCode:     statusCode,
						ErrorKind:      upstreamErrorKind(statusCode, data, codex429Decision{}),
						EffectiveModel: effectiveModel,
						Message:        usageLogErrorMessage(statusCode, data),
					})
					if shouldHoldRetryableResponseFailed(data, attempt, maxRetries, wroteAnyBody, c.Request.Context().Err()) {
						return false
					}
					if err := streamWriter.WriteString(anthropicStreamErrorForResponseFailed(data)); err != nil {
						writeErr = err
					} else {
						wroteAnyBody = true
					}
					return false
				}

				if !wroteAnyBody && isResponsesPreambleEvent(eventType) {
					return true
				}

				// 翻译并写入
				events := translator.translateEvent(data)
				for _, evt := range events {
					sse := anthropicEventToSSE(evt)
					if err := streamWriter.WriteString(sse); err != nil {
						writeErr = err
						return false
					}
					wroteAnyBody = true
				}

				return eventType != "response.completed" && eventType != "response.failed"
			})
			if err := stopKeepalive(); writeErr == nil {
				writeErr = err
			}
			if writeErr == nil {
				writeErr = streamWriter.Flush()
			}

			if !gotTerminal && writeErr == nil && (attempt >= maxRetries || wroteAnyBody) {
				message := "上游流提前结束，未收到 response.completed 或 response.failed"
				if readErr != nil {
					message = fmt.Sprintf("上游流读取失败: %v", readErr)
				}
				if err := streamWriter.WriteString(anthropicStreamErrorSSE("api_error", message)); err != nil {
					writeErr = err
				} else {
					wroteAnyBody = true
				}
			}
			if writeErr == nil {
				writeErr = streamWriter.Flush()
			}

			// 流正常 EOF 时补齐事件；异常断流不能假装正常完成。
			if writeErr == nil && readErr == nil {
				finalEvents := translator.finalize()
				// 仅在 message_stop 未发送过时输出
				if !gotTerminal {
					for _, evt := range finalEvents {
						sse := anthropicEventToSSE(evt)
						if err := streamWriter.WriteString(sse); err != nil {
							writeErr = err
							break
						}
					}
					if writeErr == nil {
						writeErr = streamWriter.Flush()
					}
				}
			}
		} else {
			// 非流式：缓冲所有事件后构建完整 JSON 响应
			var lastCompletedData []byte

			readErr = ReadSSEStream(resp.Body, func(data []byte) bool {
				parsed := gjson.ParseBytes(data)
				eventType := parsed.Get("type").String()
				if !firstSSETraced {
					firstSSETraced = true
					h.traceRequestEvent(c, trace, "first_sse", requestTraceFields{
						AccountID:      account.ID(),
						Attempt:        attempt + 1,
						EffectiveModel: effectiveModel,
						Message:        eventType,
					})
				}

				if !ttftRecorded && isFirstTokenEvent(eventType) {
					firstTokenMs = int(time.Since(start).Milliseconds())
					ttftRecorded = true
				}
				if !firstContentTraced && isFirstTokenEvent(eventType) {
					firstContentTraced = true
					h.traceRequestEvent(c, trace, "first_content", requestTraceFields{
						AccountID:      account.ID(),
						Attempt:        attempt + 1,
						EffectiveModel: effectiveModel,
						Message:        eventType,
					})
				}
				if eventType == "response.output_text.delta" || eventType == "response.function_call_arguments.delta" {
					deltaCharCount += len(parsed.Get("delta").String())
				}
				if eventType == "response.completed" {
					usage = extractUsageFromResult(parsed.Get("response.usage"))
					lastCompletedData = data
					gotTerminal = true
					return false
				}
				if eventType == "response.failed" {
					terminalFailurePayload = append([]byte(nil), data...)
					gotTerminal = true
					statusCode := responseFailedStatusCode(data)
					h.traceRequestEvent(c, trace, "response_failed", requestTraceFields{
						AccountID:      account.ID(),
						Attempt:        attempt + 1,
						StatusCode:     statusCode,
						ErrorKind:      upstreamErrorKind(statusCode, data, codex429Decision{}),
						EffectiveModel: effectiveModel,
						Message:        usageLogErrorMessage(statusCode, data),
					})
					return false
				}
				return true
			})

			if lastCompletedData != nil {
				anthropicResp := buildAnthropicResponseFromCompleted(lastCompletedData, originalModel)
				c.JSON(http.StatusOK, anthropicResp)
			} else {
				sendAnthropicError(c, http.StatusBadGateway, "api_error", "No complete response received from upstream")
			}
		}

		// 断流检测 + token 估算
		totalDuration := int(time.Since(start).Milliseconds())
		outcome := classifyStreamOutcome(c.Request.Context().Err(), readErr, writeErr, gotTerminal)
		if len(terminalFailurePayload) > 0 {
			outcome = classifyResponseFailedOutcome(terminalFailurePayload)
		}
		if shouldTransparentRetryStream(outcome, attempt, maxRetries, wroteAnyBody, c.Request.Context().Err(), writeErr) {
			log.Printf("上游流在首包前断开，重试 (attempt %d/%d, account %d, /v1/messages): %s",
				attempt+1, maxRetries+1, account.ID(), outcome.failureMessage)
			h.traceRequestEvent(c, trace, "retry_scheduled", requestTraceFields{
				AccountID:      account.ID(),
				Attempt:        attempt + 1,
				StatusCode:     outcome.logStatusCode,
				ErrorKind:      outcome.failureKind,
				EffectiveModel: effectiveModel,
				Message:        outcome.failureMessage,
			})
			recyclePooledClient(account, proxyURL)
			if usagePct, ok := parseCodexUsageHeaders(resp, account); ok {
				h.store.PersistUsageSnapshot(account, usagePct)
			}
			h.store.ReportRequestFailure(account, outcome.failureKind, time.Duration(totalDuration)*time.Millisecond)
			resp.Body.Close()
			h.store.Release(account)
			h.store.UnbindSessionAffinity(affinityKey, account.ID())
			continue
		}

		h.store.BindSessionAffinity(affinityKey, account, proxyURL)

		logStatusCode := outcome.logStatusCode
		if outcome.logStatusCode != http.StatusOK {
			log.Printf("流异常结束 (account %d, /v1/messages, status %d): %s，已转发约 %d 字符",
				account.ID(), outcome.logStatusCode, outcome.failureMessage, deltaCharCount)
			h.traceRequestEvent(c, trace, "upstream_error", requestTraceFields{
				AccountID:      account.ID(),
				Attempt:        attempt + 1,
				StatusCode:     outcome.logStatusCode,
				ErrorKind:      outcome.failureKind,
				EffectiveModel: effectiveModel,
				Message:        outcome.failureMessage,
			})
			if deltaCharCount > 0 {
				estOutputTokens := deltaCharCount / 3
				if estOutputTokens < 1 {
					estOutputTokens = 1
				}
				usage = &UsageInfo{
					OutputTokens:     estOutputTokens,
					CompletionTokens: estOutputTokens,
					TotalTokens:      estOutputTokens,
				}
			}
		}

		logInput := &database.UsageLogInput{
			AccountID:        account.ID(),
			Endpoint:         "/v1/messages",
			Model:            model,
			EffectiveModel:   effectiveModel,
			StatusCode:       logStatusCode,
			DurationMs:       totalDuration,
			FirstTokenMs:     firstTokenMs,
			ReasoningEffort:  reasoningEffort,
			InboundEndpoint:  "/v1/messages",
			UpstreamEndpoint: "/v1/responses",
			Stream:           isStream,
		}
		if logStatusCode != http.StatusOK {
			logInput.ErrorMessage = usageLogErrorMessage(logStatusCode, []byte(outcome.failureMessage))
			logInput.UpstreamErrorKind = outcome.failureKind
		}
		if usage != nil {
			logInput.PromptTokens = usage.PromptTokens
			logInput.CompletionTokens = usage.CompletionTokens
			logInput.TotalTokens = usage.TotalTokens
			logInput.InputTokens = usage.InputTokens
			logInput.OutputTokens = usage.OutputTokens
			logInput.ReasoningTokens = usage.ReasoningTokens
			logInput.CachedTokens = usage.CachedTokens
		}
		h.logUsageForRequest(c, logInput)
		traceTerminal = true
		if logStatusCode == http.StatusOK {
			h.traceRequestEvent(c, trace, "request_completed", requestTraceFields{
				AccountID:      account.ID(),
				Attempt:        attempt + 1,
				StatusCode:     http.StatusOK,
				EffectiveModel: effectiveModel,
				Message:        fmt.Sprintf("duration_ms=%d first_token_ms=%d", totalDuration, firstTokenMs),
			})
		} else {
			h.traceRequestEvent(c, trace, "request_failed", requestTraceFields{
				AccountID:      account.ID(),
				Attempt:        attempt + 1,
				StatusCode:     logStatusCode,
				ErrorKind:      outcome.failureKind,
				EffectiveModel: effectiveModel,
				Message:        outcome.failureMessage,
			})
		}

		resp.Body.Close()
		if usagePct, ok := parseCodexUsageHeaders(resp, account); ok {
			h.store.PersistUsageSnapshot(account, usagePct)
		}
		if outcome.penalize {
			recyclePooledClient(account, proxyURL)
			h.store.ReportRequestFailure(account, outcome.failureKind, time.Duration(totalDuration)*time.Millisecond)
			h.store.UnbindSessionAffinity(affinityKey, account.ID())
		} else if outcome.logStatusCode == http.StatusOK {
			h.store.ClearModelCooldown(account, effectiveModel)
			h.store.ReportRequestSuccess(account, time.Duration(totalDuration)*time.Millisecond)
		}
		h.store.Release(account)
		return
	}
}
