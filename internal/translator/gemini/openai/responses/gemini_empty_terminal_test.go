package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestGeminiEmptyTerminalIsFailureWithoutRewritingMeter(t *testing.T) {
	for _, meter := range []string{"", `,"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":0,"cachedContentTokenCount":90}`} {
		for _, finish := range []string{"", `,"finishReason":"STOP"`} {
			raw := `{"candidates":[{"content":{"parts":[]}` + finish + `}]` + meter + `}`
			var state any
			events := issue6258Translate(t, &state, raw)
			events = append(events, issue6258Translate(t, &state, "[DONE]")...)
			for _, response := range []gjson.Result{
				issue6258Terminal(t, events),
				gjson.ParseBytes(ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-4-argon", nil, nil, []byte(raw), nil)),
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
