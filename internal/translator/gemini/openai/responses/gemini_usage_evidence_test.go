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

func TestGeminiUsageEvidenceTerminalCacheCorrection(t *testing.T) {
	const start = `{"candidates":[{"content":{"parts":[{"text":"synthetic answer"}]}}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":1,"totalTokenCount":101,"cachedContentTokenCount":90}}`
	cases := []struct {
		name, finish, metadata string
		cache, output          int64
	}{
		{"complete_positive", "STOP", `{"promptTokenCount":100,"candidatesTokenCount":15,"totalTokenCount":115,"cachedContentTokenCount":0}`, 0, 15},
		{"zero_output_preserves_observed_output", "STOP", `{"promptTokenCount":100,"candidatesTokenCount":0,"totalTokenCount":100,"cachedContentTokenCount":0}`, 0, 1},
		{"positive_cache_correction", "STOP", `{"promptTokenCount":100,"candidatesTokenCount":15,"totalTokenCount":115,"cachedContentTokenCount":30}`, 30, 15},
		{"total_sum_overflow", "STOP", `{"promptTokenCount":100,"candidatesTokenCount":9223372036854775807,"totalTokenCount":99,"cachedContentTokenCount":0}`, 90, 9223372036854775807},
		{"nonterminal", "", `{"promptTokenCount":100,"candidatesTokenCount":15,"totalTokenCount":115,"cachedContentTokenCount":0}`, 90, 15},
		{"unspecified_finish", "FINISH_REASON_UNSPECIFIED", `{"promptTokenCount":100,"candidatesTokenCount":15,"totalTokenCount":115,"cachedContentTokenCount":0}`, 90, 15},
		{"partial", "STOP", `{"candidatesTokenCount":15,"totalTokenCount":115,"cachedContentTokenCount":0}`, 90, 15},
		{"missing_total", "STOP", `{"promptTokenCount":100,"candidatesTokenCount":15,"cachedContentTokenCount":0}`, 90, 15},
		{"inconsistent_total", "STOP", `{"promptTokenCount":100,"candidatesTokenCount":15,"totalTokenCount":114,"cachedContentTokenCount":0}`, 90, 15},
		{"string_cache", "STOP", `{"promptTokenCount":100,"candidatesTokenCount":15,"totalTokenCount":115,"cachedContentTokenCount":"0"}`, 90, 15},
		{"invalid_thoughts", "STOP", `{"promptTokenCount":100,"candidatesTokenCount":15,"thoughtsTokenCount":null,"totalTokenCount":115,"cachedContentTokenCount":0}`, 90, 15},
		{"all_zero", "STOP", `{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0,"cachedContentTokenCount":0}`, 90, 1},
		{"stale_prompt", "STOP", `{"promptTokenCount":90,"candidatesTokenCount":15,"totalTokenCount":105,"cachedContentTokenCount":0}`, 90, 15},
	}
	for _, tc := range cases {
		for _, field := range []string{"usageMetadata", "cpaUsageMetadata"} {
			t.Run(tc.name+"/"+field, func(t *testing.T) {
				t.Parallel()
				finish := ""
				if tc.finish != "" {
					finish = `"candidates":[{"finishReason":"` + tc.finish + `"}],`
				}
				response := completedGeminiMeteringResponse(t, start, `{`+finish+`"`+field+`":`+tc.metadata+`}`, "[DONE]")
				if got := response.Get("usage.input_tokens_details.cached_tokens").Int(); got != tc.cache {
					t.Errorf("cache=%d, want=%d", got, tc.cache)
				}
				if got := response.Get("usage.output_tokens").Int(); got != tc.output {
					t.Errorf("output=%d, want=%d", got, tc.output)
				}
				if got := response.Get("usage.input_tokens").Int(); got != 100 {
					t.Errorf("input=%d, want=100", got)
				}
			})
		}
	}
	for _, tc := range []struct {
		name, metadata string
		cache          int64
	}{
		{"complete", `{"promptTokenCount":100,"candidatesTokenCount":15,"totalTokenCount":115,"cachedContentTokenCount":0}`, 0},
		{"all_zero", `{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0,"cachedContentTokenCount":0}`, 90},
		{"partial", `{"promptTokenCount":100,"cachedContentTokenCount":0}`, 90},
		{"placeholder", `{}`, 90},
	} {
		t.Run("usage_after_finish/"+tc.name, func(t *testing.T) {
			response := completedGeminiMeteringResponse(t, start, `{"candidates":[{"finishReason":"STOP"}]}`, `{"usageMetadata":`+tc.metadata+`}`, "[DONE]")
			if got := response.Get("usage.input_tokens_details.cached_tokens").Int(); got != tc.cache {
				t.Errorf("cache=%d, want=%d", got, tc.cache)
			}
		})
	}
	t.Run("preserve_observed_reasoning", func(t *testing.T) {
		initial := `{"candidates":[{"content":{"parts":[{"text":"synthetic answer"}]}}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":1,"thoughtsTokenCount":5,"totalTokenCount":106,"cachedContentTokenCount":90}}`
		terminal := `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":0,"thoughtsTokenCount":0,"totalTokenCount":100,"cachedContentTokenCount":0}}`
		response := completedGeminiMeteringResponse(t, initial, terminal, "[DONE]")
		for field, want := range map[string]int64{"input_tokens": 100, "output_tokens": 6, "output_tokens_details.reasoning_tokens": 5, "total_tokens": 106, "input_tokens_details.cached_tokens": 0} {
			if got := response.Get("usage." + field).Int(); got != want {
				t.Errorf("%s=%d, want=%d", field, got, want)
			}
		}
	})
	for _, cached := range []string{"0", "30"} {
		t.Run("nonstream_cache_"+cached, func(t *testing.T) {
			raw := `{"candidates":[{"content":{"parts":[{"text":"synthetic answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":15,"totalTokenCount":115,"cachedContentTokenCount":` + cached + `}}`
			response := gjson.ParseBytes(ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-4-argon", nil, nil, []byte(raw), nil))
			if response.Get("usage.input_tokens_details.cached_tokens").Raw != cached || response.Get("usage.input_tokens").Int() != 100 || response.Get("usage.output_tokens").Int() != 15 {
				t.Fatal("nonstream terminal usage changed")
			}
		})
	}
}

func TestGeminiUsageEvidenceInitialZeroNeedsSourceFinish(t *testing.T) {
	const initial = `{"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}}`
	const content = `{"candidates":[{"content":{"parts":[{"text":"synthetic answer"}]}}]}`
	for _, tc := range []struct {
		name   string
		frames []string
	}{
		{"source_finish_without_usage", []string{initial, content, `{"candidates":[{"finishReason":"STOP"}]}`, "[DONE]"}},
		{"synthetic_finish_only", []string{initial, content, "[DONE]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := completedGeminiMeteringResponse(t, tc.frames...)
			if response.Get("usage").Exists() {
				t.Fatal("initial zero usage became authoritative terminal evidence")
			}
		})
	}
	t.Run("confirmed_late_zero", func(t *testing.T) {
		response := completedGeminiMeteringResponse(t, content, `{"candidates":[{"finishReason":"STOP"}]}`, initial, "[DONE]")
		if !response.Get("usage").Exists() || response.Get("usage.total_tokens").Int() != 0 {
			t.Fatal("complete zero usage after source finish was lost")
		}
	})
	t.Run("nonstream_final_body", func(t *testing.T) {
		response := gjson.ParseBytes(ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-4-argon", nil, nil, []byte(initial), nil))
		if !response.Get("usage").Exists() || response.Get("usage.total_tokens").Int() != 0 {
			t.Fatal("complete zero usage in final nonstream body was lost")
		}
	})
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
