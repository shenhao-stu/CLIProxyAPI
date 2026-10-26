package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestGeminiPartialMeterDoesNotInventCounters(t *testing.T) {
	for _, tc := range []struct {
		name, meter, absent, known string
		value                      int64
	}{
		{"input_only", `{"promptTokenCount":100}`, "output_tokens", "input_tokens", 100},
		{"output_only", `{"candidatesTokenCount":3}`, "input_tokens", "output_tokens", 3},
		{"thoughts_only", `{"thoughtsTokenCount":3}`, "output_tokens", "output_tokens_details.reasoning_tokens", 3},
		{"cache_only", `{"cachedContentTokenCount":90}`, "input_tokens", "input_tokens_details.cached_tokens", 90},
		{"total_only", `{"totalTokenCount":100}`, "output_tokens", "total_tokens", 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"candidates":[{"content":{"parts":[{"text":"synthetic"}]},"finishReason":"STOP"}],"usageMetadata":` + tc.meter + `}`
			for _, response := range []gjson.Result{
				completedGeminiMeteringResponse(t, raw, "[DONE]"),
				gjson.ParseBytes(ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-4-argon", nil, nil, []byte(raw), nil)),
			} {
				if response.Get("usage." + tc.absent).Exists() {
					t.Errorf("missing native %s became a fabricated counter: %s", tc.absent, response.Get("usage").Raw)
				}
				if v := response.Get("usage." + tc.known); !v.Exists() || v.Int() != tc.value {
					t.Errorf("observed counter was lost: %s", response.Get("usage").Raw)
				}
			}
		})
	}
}

func TestGeminiPartialFinishWaitsForFinalMeter(t *testing.T) {
	var state any
	start := `{"candidates":[{"content":{"parts":[{"text":"synthetic"}]}}]}`
	events := issue6258Translate(t, &state, start)
	partial := issue6258Translate(t, &state, `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100}}`)
	for _, event := range partial {
		if event.Get("type").String() == "response.completed" {
			t.Error("partial meter finalized the stream before its final usage tail")
		}
	}
	events = append(events, partial...)
	events = append(events, issue6258Translate(t, &state, `{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":15,"totalTokenCount":115}}`)...)
	events = append(events, issue6258Translate(t, &state, "[DONE]")...)
	response := issue6258Terminal(t, events)
	if response.Get("usage.output_tokens").Int() != 15 {
		t.Fatalf("final output meter lost: %s", response.Get("usage").Raw)
	}
}

func TestGeminiZeroPrimerCannotCompletePartialFinalMeter(t *testing.T) {
	response := completedGeminiMeteringResponse(t,
		`{"candidates":[{"content":{"parts":[{"text":"synthetic"}]}}],"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0}}`,
		`{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100}}`, "[DONE]")
	if response.Get("usage.output_tokens").Exists() || response.Get("usage.input_tokens").Int() != 100 {
		t.Fatalf("zero primer fabricated a final output meter: %s", response.Get("usage").Raw)
	}
}
