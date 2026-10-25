package responses

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAntigravityNonContentTerminalReason(t *testing.T) {
	for _, reason := range []string{"SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "MALFORMED_RESPONSE", "MALFORMED_FUNCTION_CALL", "MAX_TOKENS", "STOP"} {
		for _, mode := range []string{"buffered", "stream", "late_usage"} {
			t.Run(reason+"/"+mode, func(t *testing.T) {
				usage := `"usageMetadata":{"promptTokenCount":19,"candidatesTokenCount":2,"thoughtsTokenCount":3,"cachedContentTokenCount":7,"totalTokenCount":24}`
				terminal := fmt.Sprintf(`{"candidates":[{"finishReason":%q}],%s,"responseId":"synthetic-terminal"}`, reason, usage)
				wantStatus, wantReason := "incomplete", "content_filter"
				if strings.HasPrefix(reason, "MALFORMED_") {
					wantStatus, wantReason = "failed", ""
				} else if reason == "STOP" {
					wantStatus, wantReason = "completed", ""
				} else if reason == "MAX_TOKENS" {
					wantReason = "max_output_tokens"
				}
				var final gjson.Result
				if mode == "buffered" {
					data := []byte(`{"response":` + terminal + `}`)
					final = gjson.ParseBytes(ConvertAntigravityResponseToOpenAIResponsesNonStream(context.Background(), "gemini-4-argon", nil, nil, data, nil))
				} else {
					frames := []string{`{"response":` + terminal + `}`, "[DONE]"}
					if mode == "late_usage" {
						frames = []string{fmt.Sprintf(`{"response":{"candidates":[{"finishReason":%q}],"responseId":"synthetic-terminal"}}`, reason), `{"response":{` + usage + `}}`, "[DONE]"}
					}
					var state any
					terminals := 0
					for _, frame := range frames {
						for _, chunk := range ConvertAntigravityResponseToOpenAIResponses(context.Background(), "gemini-4-argon", nil, nil, []byte(frame), &state) {
							for _, line := range strings.Split(string(chunk), "\n") {
								if !strings.HasPrefix(line, "data:") {
									continue
								}
								event := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
								if kind := event.Get("type").String(); kind == "response.completed" || kind == "response.incomplete" || kind == "response.failed" {
									terminals++
									final = event.Get("response")
									if kind != "response."+wantStatus {
										t.Errorf("terminal event %s, want %s", kind, wantStatus)
									}
								}
							}
						}
					}
					if terminals != 1 {
						t.Fatalf("terminal count %d", terminals)
					}
				}
				if final.Get("status").String() != wantStatus || final.Get("incomplete_details.reason").String() != wantReason {
					t.Errorf("lost terminal reason: status=%s reason=%s", final.Get("status"), final.Get("incomplete_details.reason"))
				}
				if wantStatus == "failed" && (final.Get("error.type").String() != "server_error" || final.Get("error.status_code").Int() != 502 || !strings.HasPrefix(final.Get("error.code").String(), "upstream_malformed_")) {
					t.Errorf("format error lost: %s", final.Get("error"))
				}
				if final.Get("usage.input_tokens").Int() != 19 || final.Get("usage.output_tokens").Int() != 5 || final.Get("usage.input_tokens_details.cached_tokens").Int() != 7 {
					t.Errorf("final meter changed: %s", final.Get("usage"))
				}
				if len(final.Get("output").Array()) != 0 {
					t.Fatal("empty upstream output became invented content")
				}
			})
		}
	}
}
