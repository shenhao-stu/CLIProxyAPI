package helps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func startedGeminiProgress(t *testing.T) GeminiResponsesProgress {
	t.Helper()
	p := NewGeminiResponsesProgress("gemini-4-argon-high", sdktranslator.FormatOpenAIResponse)
	created := []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n")
	got := p.Forward([]byte(`{"candidates":[{}]}`), [][]byte{created})
	if len(got) != 1 || !bytes.Equal(got[0], created) {
		t.Fatal("bootstrap data changed")
	}
	return p
}

func TestGeminiResponsesProgressBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"comment", ": upstream private text", true},
		{"thought", `{"candidates":[{"content":{"parts":[{"thought":true,"text":"progress"}]}}]}`, true},
		{"signature", `{"candidates":[{"content":{"parts":[{"thoughtSignature":"private-signature"}]}}]}`, true},
		{"usage", `{"usageMetadata":{"thoughtsTokenCount":3}}`, true},
		{"wrapped_usage", `{"response":{"usageMetadata":{"candidatesTokenCount":2}}}`, true},
		{"empty", "", false},
		{"newline", "\n", false},
		{"event_line", "event: progress", false},
		{"unknown_json", `{"unknown":"value"}`, false},
		{"partial_json", `{"candidates":[`, false},
		{"malformed_counter", `{"usageMetadata":{"thoughtsTokenCount":"3"}}`, false},
		{"negative_counter", `{"usageMetadata":{"thoughtsTokenCount":-1}}`, false},
		{"empty_metadata", `{"usageMetadata":{}}`, false},
		{"other_candidate", `{"candidates":[{}, {"content":{"parts":[{"text":"ignored candidate"}]}}]}`, false},
		{"source_done", "data: [DONE]", false},
		{"source_stop", `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{}}`, false},
		{"source_error", `{"error":{"code":500}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := startedGeminiProgress(t)
			got := p.Forward([]byte(tc.raw), nil)
			if (len(got) == 1) != tc.want || len(got) > 1 {
				t.Fatalf("activity chunks=%d want activity=%v", len(got), tc.want)
			}
			if tc.want && string(got[0]) != ": upstream-activity\n\n" {
				t.Fatalf("private or invented payload escaped: %q", got)
			}
		})
	}
}

func TestGeminiResponsesProgressScopeAndLifecycle(t *testing.T) {
	for _, tc := range []struct {
		model  string
		format sdktranslator.Format
	}{
		{"claude-sonnet-4-5", sdktranslator.FormatOpenAIResponse},
		{"gemini-4-argon", sdktranslator.FormatOpenAI},
		{"gemini-4-argon", sdktranslator.FormatGemini},
	} {
		p := NewGeminiResponsesProgress(tc.model, tc.format)
		p.Forward(nil, [][]byte{[]byte(`data: {"type":"response.created"}`)})
		if got := p.Forward([]byte(": heartbeat"), nil); len(got) != 0 {
			t.Fatalf("activity added outside Gemini Responses: %s/%s", tc.model, tc.format)
		}
	}
	p := NewGeminiResponsesProgress("gemini-4-argon", sdktranslator.FormatOpenAIResponse)
	for range 1000 {
		if got := p.Forward([]byte(": pre-header"), nil); len(got) != 0 {
			t.Fatal("comment before data could commit HTTP or accumulate bootstrap memory")
		}
	}
	for _, terminal := range []string{
		`{"candidates":[{"finishReason":"STOP"}]}`,
		`{"candidates":[{"finishReason":"MAX_TOKENS"}]}`,
		`{"error":{"code":500}}`,
		"[DONE]",
	} {
		p = startedGeminiProgress(t)
		p.Forward([]byte(terminal), nil)
		if got := p.Forward([]byte(": after terminal"), nil); len(got) != 0 {
			t.Fatalf("source terminal allowed comments to prolong missing-usage wait: %s", terminal)
		}
		late := []byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":9,"output_tokens":2,"total_tokens":11}}}`)
		if got := p.Forward([]byte(`{"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":2}}`), [][]byte{late}); len(got) != 1 || !bytes.Equal(got[0], late) {
			t.Fatal("comment fence discarded or changed late usage/completion")
		}
	}
	for _, kind := range []string{"response.completed", "response.incomplete", "response.failed", "error"} {
		p = startedGeminiProgress(t)
		p.Forward(nil, [][]byte{[]byte(fmt.Sprintf(`data: {"type":%q}`, kind))})
		if got := p.Forward([]byte(": after terminal"), nil); len(got) != 0 {
			t.Fatalf("downstream terminal %s allowed further comments", kind)
		}
	}
}

func TestGeminiResponsesProgressDoesNotMultiplyOrInventData(t *testing.T) {
	p := startedGeminiProgress(t)
	usage := []byte(`{"usageMetadata":{"thoughtsTokenCount":3}}`)
	if len(p.Forward(usage, nil)) != 1 || len(p.Forward(usage, nil)) != 0 {
		t.Fatal("unchanged usage must not become continuing progress")
	}
	if len(p.Forward([]byte(`{"usageMetadata":{"thoughtsTokenCount":4}}`), nil)) != 1 {
		t.Fatal("new usage counter progress was lost")
	}
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"x"}]}}` + strings.Repeat(`,{"content":{"parts":[{"text":"x"}]}}`, 99) + `]}`)
	if got := p.Forward(raw, nil); len(got) != 1 {
		t.Fatalf("multiple candidates amplified heartbeat output: %d", len(got))
	}
	chunk := []byte(`data: {"type":"response.output_text.delta","delta":"x"}`)
	if got := p.Forward(raw, [][]byte{chunk}); len(got) != 1 || !bytes.Equal(got[0], chunk) {
		t.Fatal("normal output changed or gained a redundant comment")
	}
}

func TestGeminiResponsesProgressSafeOutcome(t *testing.T) {
	logger := log.StandardLogger()
	oldLevel, oldOutput := logger.GetLevel(), logger.Out
	oldHooks := logger.ReplaceHooks(make(log.LevelHooks))
	logger.SetLevel(log.InfoLevel)
	logger.SetOutput(io.Discard)
	hook := logtest.NewLocal(logger)
	t.Cleanup(func() {
		logger.ReplaceHooks(oldHooks)
		logger.SetLevel(oldLevel)
		logger.SetOutput(oldOutput)
	})
	p := startedGeminiProgress(t)
	p.LogOutcome(context.Background(), nil)
	if len(hook.AllEntries()) != 0 {
		t.Fatal("normal stream without progress should not log a summary")
	}
	p.LogOutcome(context.Background(), errors.New("private https://source.invalid/key"))
	entry := hook.LastEntry()
	if entry == nil || entry.Data["outcome"] != "read_error" || entry.Data["emitted_progress_count"] != uint64(0) || entry.Data["native_terminal_seen"] != false {
		t.Fatalf("silent upstream failure missing safe zero-count diagnosis: %#v", entry)
	}
	if encoded, _ := entry.String(); strings.Contains(encoded, "private") || strings.Contains(encoded, "source.invalid") {
		t.Fatalf("source error detail leaked in activity log: %s", encoded)
	}
	hook.Reset()
	p.Forwarded([]byte(": upstream-activity\n\n"))
	p.Forwarded([]byte("data: other\n\n"))
	p.Forward([]byte(`{"candidates":[{"finishReason":"STOP"}]}`), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.LogOutcome(ctx, nil)
	entry = hook.LastEntry()
	if entry == nil || entry.Data["outcome"] != "canceled" || entry.Data["emitted_progress_count"] != uint64(1) || entry.Data["native_terminal_seen"] != true || entry.Data["translated_terminal_seen"] != false {
		t.Fatalf("terminal waiting/cancellation summary inaccurate: %#v", entry)
	}
}

func TestGeminiResponsesProgressProductionFormatter(t *testing.T) {
	logger := log.StandardLogger()
	oldLevel, oldOutput, oldFormatter := logger.GetLevel(), logger.Out, logger.Formatter
	oldHooks := logger.ReplaceHooks(make(log.LevelHooks))
	var output bytes.Buffer
	logger.SetLevel(log.InfoLevel)
	logger.SetOutput(&output)
	logger.SetFormatter(&logging.LogFormatter{})
	t.Cleanup(func() {
		logger.ReplaceHooks(oldHooks)
		logger.SetLevel(oldLevel)
		logger.SetOutput(oldOutput)
		logger.SetFormatter(oldFormatter)
	})

	for _, tc := range []struct {
		name, outcome               string
		emitted, native, translated bool
		usage                       string
		usageFields                 uint8
		canceled                    bool
	}{
		{name: "read_error", outcome: "read_error"},
		{name: "canceled_waiting_usage", outcome: "canceled", emitted: true, native: true, canceled: true},
		{name: "partial_usage", outcome: "canceled", native: true, usage: `{"usageMetadata":{"promptTokenCount":12}}`, usageFields: 1, canceled: true},
		{name: "completed_missing_usage", outcome: "closed", emitted: true, native: true, translated: true},
		{name: "completed_known_zero", outcome: "closed", emitted: true, native: true, translated: true, usage: `{"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0}}`, usageFields: 3},
		{name: "completed_then_context_canceled", outcome: "closed", emitted: true, native: true, translated: true, usage: `{"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":4}}`, usageFields: 3, canceled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output.Reset()
			p := startedGeminiProgress(t)
			if tc.emitted {
				for _, chunk := range p.Forward([]byte(": private-native-comment"), nil) {
					p.Forwarded(chunk)
				}
			}
			if tc.native {
				p.Forward([]byte(`{"candidates":[{"finishReason":"STOP"}]}`), nil)
			}
			if tc.usage != "" {
				p.Forward([]byte(tc.usage), nil)
			}
			if tc.translated {
				p.Forward(nil, [][]byte{[]byte(`data: {"type":"response.completed"}`)})
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var readErr error
			if tc.outcome == "read_error" {
				readErr = errors.New("private-error https://source.invalid/private-key")
			}
			if tc.canceled {
				cancel()
			}
			p.LogOutcome(ctx, readErr)
			count := 0
			if tc.emitted {
				count = 1
			}
			for _, want := range []string{
				fmt.Sprintf("emitted_progress_count=%d", count),
				"responses_started=true",
				fmt.Sprintf("native_terminal_seen=%t", tc.native),
				fmt.Sprintf("translated_terminal_seen=%t", tc.translated),
				fmt.Sprintf("native_usage_fields_seen=%d", tc.usageFields),
				fmt.Sprintf("context_canceled=%t", tc.canceled),
				"outcome=" + tc.outcome,
			} {
				if !strings.Contains(output.String(), want) {
					t.Errorf("serialized activity summary %q missing %s", output.String(), want)
				}
			}
			if strings.Contains(output.String(), "private") || strings.Contains(output.String(), "https://") {
				t.Fatalf("activity summary leaked source detail: %q", output.String())
			}
		})
	}

	output.Reset()
	log.WithFields(log.Fields{
		"raw_error":    "private-error",
		"upstream_url": "https://source.invalid/private-key",
		"request_body": "private-customer-body",
		"unknown":      "private-arbitrary-field",
	}).Info("unknown fields remain omitted")
	if strings.Contains(output.String(), "private") || strings.Contains(output.String(), "https://") {
		t.Fatalf("formatter expanded beyond the fixed allowlist: %q", output.String())
	}
}
