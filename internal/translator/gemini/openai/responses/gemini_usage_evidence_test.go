package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestGeminiUsageEvidence(t *testing.T) {
	cases := []struct {
		name, metadata string
		present        bool
	}{
		{"complete_zero", `{"promptTokenCount":0,"candidatesTokenCount":0,"thoughtsTokenCount":0,"totalTokenCount":0,"cachedContentTokenCount":0}`, true},
		{"minimal_zero", `{"promptTokenCount":0,"candidatesTokenCount":0}`, true},
		{"positive", `{"promptTokenCount":100,"candidatesTokenCount":2,"totalTokenCount":102,"cachedContentTokenCount":90}`, true},
		{"positive_partial", `{"promptTokenCount":100}`, true},
		{"thoughts_only", `{"thoughtsTokenCount":3}`, true},
		{"cache_only", `{"cachedContentTokenCount":90}`, true},
		{"total_only", `{"totalTokenCount":100}`, true},
		{"missing", ``, false},
		{"empty", `{}`, false},
		{"null", `null`, false},
		{"array", `[]`, false},
		{"unknown", `{"trafficType":"ON_DEMAND"}`, false},
		{"input_zero_only", `{"promptTokenCount":0}`, false},
		{"output_zero_only", `{"candidatesTokenCount":0}`, false},
		{"string_input", `{"promptTokenCount":"0","candidatesTokenCount":0}`, false},
		{"boolean_input", `{"promptTokenCount":false,"candidatesTokenCount":0}`, false},
		{"null_input", `{"promptTokenCount":null,"candidatesTokenCount":0}`, false},
		{"negative_input", `{"promptTokenCount":-1,"candidatesTokenCount":0}`, false},
		{"fractional_input", `{"promptTokenCount":0.1,"candidatesTokenCount":0}`, false},
		{"overflow_input", `{"promptTokenCount":9223372036854775808,"candidatesTokenCount":0}`, false},
		{"overflow_output_sum", `{"promptTokenCount":0,"candidatesTokenCount":9223372036854775807,"thoughtsTokenCount":1}`, false},
		{"invalid_total", `{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":"0"}`, false},
		{"invalid_cache", `{"promptTokenCount":0,"candidatesTokenCount":0,"cachedContentTokenCount":null}`, false},
		{"invalid_thoughts", `{"promptTokenCount":0,"candidatesTokenCount":0,"thoughtsTokenCount":-1}`, false},
		{"duplicate_input", `{"promptTokenCount":0,"promptTokenCount":100,"candidatesTokenCount":0}`, false},
		{"duplicate_output", `{"promptTokenCount":0,"candidatesTokenCount":0,"candidatesTokenCount":10}`, false},
	}
	for _, tc := range cases {
		for _, field := range []string{"usageMetadata", "cpaUsageMetadata"} {
			for _, stream := range []bool{false, true} {
				mode := "json"
				if stream {
					mode = "sse"
				}
				t.Run(tc.name+"/"+field+"/"+mode, func(t *testing.T) {
					t.Parallel()
					raw := `{"candidates":[{"content":{"parts":[{"text":"synthetic answer"}]},"finishReason":"STOP"}]`
					if tc.metadata != "" {
						raw += `,"` + field + `":` + tc.metadata
					}
					raw += `}`
					var response gjson.Result
					if stream {
						response = completedGeminiMeteringResponse(t, raw, "[DONE]")
					} else {
						response = gjson.ParseBytes(ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-4-argon", nil, nil, []byte(raw), nil))
					}
					if got := response.Get("usage").Exists(); got != tc.present {
						t.Fatalf("usage present=%t, want=%t: %s", got, tc.present, response.Get("usage").Raw)
					}
				})
			}
		}
	}
}

func TestGeminiUsageEvidenceCumulative(t *testing.T) {
	const start = `{"responseId":"metering","candidates":[{"content":{"parts":[{"text":"synthetic answer"}]}}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":3,"cachedContentTokenCount":90,"totalTokenCount":103}}`
	for _, tail := range []string{
		`{}`,
		`null`,
		`{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0,"cachedContentTokenCount":0}`,
		`{"promptTokenCount":"0","candidatesTokenCount":null,"cachedContentTokenCount":-1}`,
		`{"promptTokenCount":120,"candidatesTokenCount":9223372036854775807,"thoughtsTokenCount":1}`,
		`{"promptTokenCount":0,"promptTokenCount":120,"candidatesTokenCount":0}`,
	} {
		t.Run(tail, func(t *testing.T) {
			t.Parallel()
			response := completedGeminiMeteringResponse(t, start, `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":`+tail+`}`, "[DONE]")
			for field, want := range map[string]int64{"input_tokens": 100, "output_tokens": 3, "total_tokens": 103, "input_tokens_details.cached_tokens": 90} {
				if got := response.Get("usage." + field).Int(); got != want {
					t.Errorf("%s=%d, want=%d", field, got, want)
				}
			}
		})
	}
	t.Run("late_cumulative_usage", func(t *testing.T) {
		response := completedGeminiMeteringResponse(t, start, `{"candidates":[{"finishReason":"STOP"}]}`, `{"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":5,"thoughtsTokenCount":2,"totalTokenCount":127}}`, "[DONE]")
		for field, want := range map[string]int64{"input_tokens": 120, "output_tokens": 7, "total_tokens": 127, "input_tokens_details.cached_tokens": 90} {
			if got := response.Get("usage." + field).Int(); got != want {
				t.Errorf("%s=%d, want=%d", field, got, want)
			}
		}
	})
	t.Run("partial_zero_frames_are_not_complete_evidence", func(t *testing.T) {
		response := completedGeminiMeteringResponse(t, `{"candidates":[{"content":{"parts":[{"text":"synthetic answer"}]}}],"usageMetadata":{"promptTokenCount":0}}`, `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"candidatesTokenCount":0}}`, "[DONE]")
		if response.Get("usage").Exists() {
			t.Fatal("separate incomplete zero frames fabricated authoritative usage")
		}
	})
}

func TestGeminiUsageEvidenceDuplicateMetadata(t *testing.T) {
	for _, field := range []string{"usageMetadata", "cpaUsageMetadata"} {
		raw := `{"candidates":[{"content":{"parts":[{"text":"synthetic answer"}]},"finishReason":"STOP"}],"` + field + `":{"promptTokenCount":0,"candidatesTokenCount":0},"` + field + `":{"promptTokenCount":100,"candidatesTokenCount":1}}`
		t.Run(field, func(t *testing.T) {
			responses := []gjson.Result{
				gjson.ParseBytes(ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-4-argon", nil, nil, []byte(raw), nil)),
				completedGeminiMeteringResponse(t, raw, "[DONE]"),
			}
			for _, response := range responses {
				if response.Get("usage").Exists() {
					t.Fatal("duplicate metadata fabricated authoritative usage")
				}
			}
			prior := `{"candidates":[{"content":{"parts":[{"text":"synthetic answer"}]}}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":3,"totalTokenCount":123}}`
			response := completedGeminiMeteringResponse(t, prior, raw, "[DONE]")
			if response.Get("usage.input_tokens").Int() != 120 || response.Get("usage.output_tokens").Int() != 3 || response.Get("usage.total_tokens").Int() != 123 {
				t.Fatal("duplicate metadata replaced previously observed usage")
			}
		})
	}
}

func completedGeminiMeteringResponse(t *testing.T, frames ...string) gjson.Result {
	t.Helper()
	var state any
	var result gjson.Result
	count := 0
	for _, frame := range frames {
		chunks := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-4-argon", nil, nil, []byte(frame), &state)
		for _, chunk := range chunks {
			for _, line := range strings.Split(string(chunk), "\n") {
				if !strings.HasPrefix(line, "data:") {
					continue
				}
				event := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				if event.Get("type").String() == "response.completed" {
					result = event.Get("response")
					count++
				}
			}
		}
	}
	if count != 1 {
		t.Fatalf("completed events=%d, want=1", count)
	}
	return result
}
