package helps

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// GeminiResponsesProgress preserves transport activity hidden by translation.
// It never invents activity during upstream silence or before response data.
type GeminiResponsesProgress struct {
	enabled, started, terminal bool
	sourceFinished             bool
	nativeText, nativeThought  bool
	nativeTool, nativeOther    bool
	nativeFinish               string
	usage                      [5]int64
	seenUsage                  uint8
	emitted                    uint64
	nativeFrames, nativeParts  uint64
	nativeSignature, emptyText bool
	promptBlocked              bool
	incompleteFrameBytes       int
	nativeErrorStatus          int
	nativeFilter               bool
}

func NewGeminiResponsesProgress(model string, format sdktranslator.Format) GeminiResponsesProgress {
	return GeminiResponsesProgress{enabled: format == sdktranslator.FormatOpenAIResponse && strings.HasPrefix(strings.ToLower(model), "gemini-")}
}

// ThinkingOnly requires a native thought and ends at the first deliverable output.
func (p *GeminiResponsesProgress) ThinkingOnly() bool {
	return p.enabled && p.nativeThought && !p.nativeText && !p.nativeTool && !p.nativeOther &&
		!p.sourceFinished && !p.terminal && !p.promptBlocked
}

func (p *GeminiResponsesProgress) Forward(raw []byte, chunks [][]byte) [][]byte {
	if !p.enabled || p.terminal {
		return chunks
	}
	active := p.observe(raw)
	for _, chunk := range chunks {
		for _, line := range bytes.Split(chunk, []byte("\n")) {
			kind := ojson.GetBytes(JSONPayload(line), "type").String()
			if strings.HasPrefix(kind, "response.") {
				p.started = true
			}
			if kind == "response.completed" || kind == "response.incomplete" || kind == "response.failed" || kind == "response.done" || kind == "error" {
				p.terminal = true
			}
		}
	}
	if p.started && !p.terminal && !p.sourceFinished && active && len(chunks) == 0 {
		return [][]byte{[]byte(": upstream-activity\n\n")}
	}
	return chunks
}

func (p *GeminiResponsesProgress) observe(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		p.sourceFinished = true
		return false
	}
	if bytes.HasPrefix(trimmed, []byte(":")) {
		return true
	}
	payload := JSONPayload(raw)
	if !ojson.ValidBytes(payload) {
		return false
	}
	root := ojson.ParseBytes(payload)
	if response := root.Get("response"); response.IsObject() {
		root = response
	}
	if !root.IsObject() {
		return false
	}
	p.nativeFrames++
	p.promptBlocked = p.promptBlocked || root.Get("promptFeedback.blockReason").Exists()
	if errFrame := root.Get("error"); errFrame.Exists() {
		p.nativeErrorStatus = int(errFrame.Get("code").Int())
		p.sourceFinished = true
		return false
	}
	active := false
	parts := root.Get("candidates.0.content.parts")
	if !parts.IsArray() {
		parts = ojson.Result{}
	}
	parts.ForEach(func(_, part ojson.Result) bool {
		p.nativeParts++
		p.nativeSignature = p.nativeSignature || part.Get("thoughtSignature").String() != "" || part.Get("thought_signature").String() != ""
		p.emptyText = p.emptyText || (part.Get("text").Type == ojson.String && strings.TrimSpace(part.Get("text").String()) == "")
		if value := part.Get("text"); value.Type == ojson.String && strings.TrimSpace(value.String()) != "" {
			if part.Get("thought").Bool() {
				p.nativeThought = true
			} else {
				p.nativeText = true
			}
		}
		p.nativeTool = p.nativeTool || part.Get("functionCall").IsObject()
		p.nativeOther = p.nativeOther || part.Get("inlineData").IsObject() || part.Get("executableCode").IsObject() || part.Get("codeExecutionResult").IsObject()
		for _, key := range []string{"text", "thoughtSignature", "thought_signature"} {
			if value := part.Get(key); value.Type == ojson.String && value.String() != "" {
				active = true
			}
		}
		if value := part.Get("functionCall"); value.IsObject() && value.Get("name").Type == ojson.String && value.Get("name").String() != "" {
			active = true
		}
		return true
	})
	if finish := root.Get("candidates.0.finishReason"); finish.Type == ojson.String {
		reason := strings.TrimSpace(finish.String())
		if reason != "" && !strings.EqualFold(reason, "FINISH_REASON_UNSPECIFIED") {
			p.sourceFinished = true
			p.nativeFinish = safeGeminiFinishReason(reason)
		}
	}
	for i, key := range []string{"promptTokenCount", "candidatesTokenCount", "thoughtsTokenCount", "cachedContentTokenCount", "totalTokenCount"} {
		value := root.Get("usageMetadata." + key)
		if !root.Get("usageMetadata").Exists() {
			value = root.Get("cpaUsageMetadata." + key)
		}
		if value.Type != ojson.Number {
			continue
		}
		count, err := strconv.ParseInt(value.Raw, 10, 64)
		if err != nil || count < 0 {
			continue
		}
		bit := uint8(1 << i)
		if p.seenUsage&bit == 0 || p.usage[i] != count {
			p.seenUsage |= bit
			p.usage[i] = count
			active = true
		}
	}
	return active
}

func safeGeminiFinishReason(reason string) string {
	switch value := strings.ToUpper(strings.TrimSpace(reason)); value {
	case "STOP", "MAX_TOKENS", "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION", "IMAGE_OTHER", "NO_IMAGE", "MALFORMED_RESPONSE", "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "TOO_MANY_TOOL_CALLS", "MISSING_THOUGHT_SIGNATURE", "LANGUAGE", "OTHER":
		return value
	default:
		return "unknown"
	}
}

// Forwarded counts only comments accepted by the downstream stream consumer.
func (p *GeminiResponsesProgress) Forwarded(chunk []byte) {
	if bytes.Equal(chunk, []byte(": upstream-activity\n\n")) {
		p.emitted++
	}
}

// FilterRejected records a verified native filter notice at stream end.
func (p *GeminiResponsesProgress) FilterRejected(status int) {
	if p.enabled {
		p.nativeErrorStatus = status
		p.nativeFilter = true
		p.sourceFinished = true
	}
}

func (p *GeminiResponsesProgress) IncompleteFrame(size int) {
	p.incompleteFrameBytes = size
}

func (p *GeminiResponsesProgress) LogOutcome(ctx context.Context, readErr error) {
	if !p.enabled {
		return
	}
	outcome := "closed"
	err := readErr
	if err == nil && !p.terminal && ctx.Err() != nil {
		err = ctx.Err()
	}
	empty := p.sourceFinished && !p.nativeText && !p.nativeThought && !p.nativeTool && !p.nativeOther
	abnormal := p.nativeFinish != "" && p.nativeFinish != "STOP"
	missingMeter := p.terminal && p.seenUsage&3 != 3
	if p.emitted == 0 && err == nil && !empty && !abnormal && !missingMeter && (!p.sourceFinished || p.terminal) {
		return
	}
	switch {
	case p.nativeErrorStatus >= 400:
		outcome = "upstream_error"
	case errors.Is(err, context.DeadlineExceeded):
		outcome = "deadline"
	case errors.Is(err, context.Canceled):
		outcome = "canceled"
	case err != nil:
		outcome = "read_error"
	}
	LogWithRequestID(ctx).WithFields(log.Fields{
		"emitted_progress_count":   p.emitted,
		"responses_started":        p.started,
		"native_terminal_seen":     p.sourceFinished,
		"native_finish_reason":     p.nativeFinish,
		"native_text_seen":         p.nativeText,
		"native_thought_seen":      p.nativeThought,
		"native_tool_seen":         p.nativeTool,
		"native_other_output_seen": p.nativeOther,
		"translated_terminal_seen": p.terminal,
		"native_usage_fields_seen": p.seenUsage,
		"context_canceled":         ctx.Err() != nil,
		"outcome":                  outcome,
		"native_frame_count":       p.nativeFrames,
		"native_part_count":        p.nativeParts,
		"native_signature_seen":    p.nativeSignature,
		"native_empty_text_seen":   p.emptyText,
		"native_prompt_blocked":    p.promptBlocked,
		"native_error_status":      p.nativeErrorStatus,
		"native_filter_notice":     p.nativeFilter,
		"incomplete_frame_bytes":   p.incompleteFrameBytes,
	}).Info("gemini responses transport activity summary")
}
