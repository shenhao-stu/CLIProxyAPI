package helps

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// GeminiResponsesProgress preserves transport activity hidden by translation.
// It never invents activity during upstream silence or before response data.
type GeminiResponsesProgress struct {
	enabled, started, terminal bool
	sourceFinished             bool
	usage                      [5]int64
	seenUsage                  uint8
	emitted                    uint64
}

func NewGeminiResponsesProgress(model string, format sdktranslator.Format) GeminiResponsesProgress {
	return GeminiResponsesProgress{enabled: format == sdktranslator.FormatOpenAIResponse && strings.HasPrefix(strings.ToLower(model), "gemini-")}
}

func (p *GeminiResponsesProgress) Forward(raw []byte, chunks [][]byte) [][]byte {
	if !p.enabled || p.terminal {
		return chunks
	}
	active := p.observe(raw)
	for _, chunk := range chunks {
		for _, line := range bytes.Split(chunk, []byte("\n")) {
			kind := gjson.GetBytes(JSONPayload(line), "type").String()
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
	if !gjson.ValidBytes(payload) {
		return false
	}
	root := gjson.ParseBytes(payload)
	if response := root.Get("response"); response.IsObject() {
		root = response
	}
	if !root.IsObject() {
		return false
	}
	if root.Get("error").Exists() {
		p.sourceFinished = true
		return false
	}
	active := false
	parts := root.Get("candidates.0.content.parts")
	if !parts.IsArray() {
		parts = gjson.Result{}
	}
	parts.ForEach(func(_, part gjson.Result) bool {
		for _, key := range []string{"text", "thoughtSignature", "thought_signature"} {
			if value := part.Get(key); value.Type == gjson.String && value.String() != "" {
				active = true
				return false
			}
		}
		if value := part.Get("functionCall"); value.IsObject() && value.Get("name").Type == gjson.String && value.Get("name").String() != "" {
			active = true
			return false
		}
		return true
	})
	if finish := root.Get("candidates.0.finishReason"); finish.Type == gjson.String {
		reason := strings.TrimSpace(finish.String())
		if reason != "" && !strings.EqualFold(reason, "FINISH_REASON_UNSPECIFIED") {
			p.sourceFinished = true
		}
	}
	for i, key := range []string{"promptTokenCount", "candidatesTokenCount", "thoughtsTokenCount", "cachedContentTokenCount", "totalTokenCount"} {
		value := root.Get("usageMetadata." + key)
		if value.Type != gjson.Number {
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

// Forwarded counts only comments accepted by the downstream stream consumer.
func (p *GeminiResponsesProgress) Forwarded(chunk []byte) {
	if bytes.Equal(chunk, []byte(": upstream-activity\n\n")) {
		p.emitted++
	}
}

func (p *GeminiResponsesProgress) LogOutcome(ctx context.Context, readErr error) {
	if !p.enabled {
		return
	}
	outcome := "closed"
	err := readErr
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if p.emitted == 0 && err == nil && (!p.sourceFinished || p.terminal) {
		return
	}
	switch {
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
		"translated_terminal_seen": p.terminal,
		"outcome":                  outcome,
	}).Info("gemini responses transport activity summary")
}
