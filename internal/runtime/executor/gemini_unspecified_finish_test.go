package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tidwall/gjson"
)

func TestGeminiResponsesUnspecifiedFinishWaitsForNativeTerminal(t *testing.T) {
	for _, provider := range []string{"gemini", "antigravity"} {
		for _, tc := range []struct {
			name, part, reason, wantText string
			reasonJSON, finish, terminal string
			tool                         bool
		}{
			{name: "text", part: `{"text":"synthetic partial"}`, reason: "FINISH_REASON_UNSPECIFIED", wantText: "synthetic partial synthetic tail"},
			{name: "case_and_space", part: `{"text":"synthetic partial"}`, reason: " finish_reason_unspecified ", wantText: "synthetic partial synthetic tail"},
			{name: "nonstring", part: `{"text":"synthetic partial"}`, reasonJSON: "7", wantText: "synthetic partial synthetic tail"},
			{name: "whitespace", part: `{"text":"synthetic partial"}`, reason: "   ", wantText: "synthetic partial synthetic tail"},
			{name: "max_tokens", part: `{"text":"synthetic partial"}`, reason: "FINISH_REASON_UNSPECIFIED", finish: "MAX_TOKENS", terminal: "response.incomplete", wantText: "synthetic partial synthetic tail"},
			{name: "safety", part: `{"text":"synthetic partial"}`, reason: "FINISH_REASON_UNSPECIFIED", finish: "SAFETY", terminal: "response.incomplete", wantText: "synthetic partial synthetic tail"},
			{name: "tool", part: `{"functionCall":{"name":"synthetic_lookup","args":{"key":"fixture"}}}`, reason: "FINISH_REASON_UNSPECIFIED", tool: true},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				var calls atomic.Int32
				reasonJSON := tc.reasonJSON
				if reasonJSON == "" {
					reasonJSON = strconv.Quote(tc.reason)
				}
				finishReason := tc.finish
				if finishReason == "" {
					finishReason = "STOP"
				}
				wantTerminal := tc.terminal
				if wantTerminal == "" {
					wantTerminal = "response.completed"
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					first := `{"responseId":"synthetic-unspecified","candidates":[{"content":{"role":"model","parts":[` + tc.part + `]},"finishReason":` + reasonJSON + `}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":1,"totalTokenCount":101,"cachedContentTokenCount":90}}`
					finalPart := `"content":{"role":"model","parts":[{"text":" synthetic tail"}]},`
					if tc.tool {
						finalPart = ""
					}
					finish := `{"candidates":[{` + finalPart + `"finishReason":"` + finishReason + `"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":7,"thoughtsTokenCount":2,"totalTokenCount":109,"cachedContentTokenCount":90}}`
					for _, frame := range []string{first, finish, `{"candidates":[{"finishReason":"STOP"}]}`} {
						_, _ = io.WriteString(w, progressSSE(provider, frame))
					}
					_, _ = io.WriteString(w, "data: [DONE]\n\n")
				}))
				defer server.Close()
				result := executeProgressStream(t, context.Background(), provider, server.URL)
				var terminals []gjson.Result
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					for _, line := range strings.Split(string(chunk.Payload), "\n") {
						if !strings.HasPrefix(line, "data: ") {
							continue
						}
						event := gjson.Parse(strings.TrimPrefix(line, "data: "))
						switch event.Get("type").String() {
						case "response.completed", "response.failed", "response.incomplete":
							terminals = append(terminals, event)
						}
					}
				}
				if calls.Load() != 1 {
					t.Fatalf("upstream dispatches=%d, want1", calls.Load())
				}
				if len(terminals) != 1 {
					t.Fatalf("terminal count=%d, want1", len(terminals))
				}
				terminal := terminals[0]
				if terminal.Get("type").String() != wantTerminal {
					t.Fatalf("unexpected terminal type=%s", terminal.Get("type").String())
				}
				for field, want := range map[string]int64{"input_tokens": 100, "output_tokens": 9, "total_tokens": 109, "input_tokens_details.cached_tokens": 90, "output_tokens_details.reasoning_tokens": 2} {
					got := terminal.Get("response.usage." + field)
					if !got.Exists() || got.Int() != want {
						t.Errorf("terminal %s=%s, want%d", field, got.Raw, want)
					}
				}
				if tc.tool {
					found := 0
					for _, item := range terminal.Get("response.output").Array() {
						if item.Get("type").String() == "function_call" {
							found++
							if item.Get("name").String() != "synthetic_lookup" || gjson.Get(item.Get("arguments").String(), "key").String() != "fixture" {
								t.Error("tool identity or arguments changed")
							}
						}
					}
					if found != 1 {
						t.Fatalf("tool output count=%d, want1", found)
					}
				} else if terminal.Get("response.output.0.content.0.text").String() != tc.wantText {
					t.Error("text tail after unspecified finish lost")
				}
			})
		}
	}
}
