package responses

import (
	"context"
	"fmt"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestGeminiThoughtOnlyWithoutNativeFinishIsFailure(t *testing.T) {
	const usage = `"usageMetadata":{"promptTokenCount":100,"totalTokenCount":100}`
	for _, tc := range []struct {
		name, part, finish, status string
	}{
		{"thought only without finish", `{"text":"synthetic reasoning","thought":true}`, "", "failed"},
		{"thought only with explicit finish", `{"text":"synthetic reasoning","thought":true}`, `,"finishReason":"STOP"`, "completed"},
		{"visible text without finish", `{"text":"answer"}`, "", "completed"},
		{"tool without finish", `{"functionCall":{"name":"lookup","id":"call_fixture","args":{"item":"test"}}}`, "", "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var state any
			frame := fmt.Sprintf(`{"candidates":[{"content":{"parts":[%s]}%s}],%s,"responseId":"fixture"}`, tc.part, tc.finish, usage)
			events := issue6258Translate(t, &state, frame)
			events = append(events, issue6258Translate(t, &state, "[DONE]")...)
			terminal := issue6258Terminal(t, events)
			if got := terminal.Get("status").String(); got != tc.status {
				t.Fatalf("status = %q, want %q", got, tc.status)
			}
			if tc.status == "failed" {
				if got := terminal.Get("error.code").String(); got != "upstream_empty_completion" {
					t.Errorf("error code = %q", got)
				}
				if terminal.Get("output.0.summary.0.text").String() != "synthetic reasoning" {
					t.Error("observed reasoning was lost")
				}
			}
			if terminal.Get("usage.input_tokens").Int() != 100 || terminal.Get("usage.output_tokens").Exists() {
				t.Error("partial input usage was lost or a missing output count was fabricated")
			}
		})
	}
}

func TestGeminiEmptyTerminalIsFailureWithoutRewritingMeter(t *testing.T) {
	for _, meter := range []string{"", `,"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":0,"cachedContentTokenCount":90}`} {
		for _, finish := range []string{"", `,"finishReason":"STOP"`} {
			raw := `{"candidates":[{"content":{"parts":[]}` + finish + `}]` + meter + `}`
			var state any
			events := issue6258Translate(t, &state, raw)
			events = append(events, issue6258Translate(t, &state, "[DONE]")...)
			for _, response := range []ojson.Result{
				issue6258Terminal(t, events),
				ojson.ParseBytes(ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-4-argon", nil, nil, []byte(raw), nil)),
			} {
				if response.Get("status").String() != "failed" || response.Get("error.code").String() != "upstream_empty_completion" || response.Get("error.status_code").Int() != 502 {
					t.Fatalf("empty response falsely completed: %s", response.Raw)
				}
				if response.Get("usage").Exists() != (meter != "") || (meter != "" && response.Get("usage.input_tokens").Int() != 100) {
					t.Fatal("empty failure invented or lost metering")
				}
			}
		}
	}
}
