package responses

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAntigravityFailedResponseItemStatus(t *testing.T) {
	for _, reason := range []string{"MALFORMED_FUNCTION_CALL", "MALFORMED_RESPONSE"} {
		for _, part := range []struct{ kind, body string }{
			{"message", `{"text":"synthetic partial answer"}`},
			{"tool_search_call", `{"functionCall":{"name":"tool_search","args":{"query":"fixture"}}}`},
		} {
			t.Run(reason+"/"+part.kind, func(t *testing.T) {
				request := []byte(`{"tools":[{"type":"tool_search","execution":"client"}]}`)
				raw := []byte(fmt.Sprintf(`{"response":{"candidates":[{"content":{"parts":[%s]},"finishReason":%q}],"usageMetadata":{"promptTokenCount":19,"candidatesTokenCount":2,"thoughtsTokenCount":3,"cachedContentTokenCount":7,"totalTokenCount":24}}}`, part.body, reason))
				check := func(t *testing.T, item gjson.Result) {
					if item.Get("type").String() == part.kind {
						if got := item.Get("status").String(); got != "completed" && got != "incomplete" && got != "in_progress" {
							t.Errorf("item status %q is outside the Responses schema", got)
						}
					}
				}
				t.Run("buffered", func(t *testing.T) {
					final := gjson.ParseBytes(ConvertAntigravityResponseToOpenAIResponsesNonStream(context.Background(), "gemini-4-argon", request, nil, raw, nil))
					if final.Get("status").String() != "failed" || len(final.Get("output").Array()) != 1 {
						t.Fatalf("failed response lost its partial item: %s", final.Raw)
					}
					item := final.Get("output.0")
					if item.Get("type").String() != part.kind || item.Get("status").String() != "incomplete" {
						t.Fatalf("partial item not retained as incomplete: %s", item.Raw)
					}
				})
				t.Run("stream", func(t *testing.T) {
					var state any
					terminals := 0
					for _, r := range [][]byte{raw, []byte("[DONE]")} {
						for _, c := range ConvertAntigravityResponseToOpenAIResponses(context.Background(), "gemini-4-argon", request, nil, r, &state) {
							for _, l := range strings.Split(string(c), "\n") {
								if !strings.HasPrefix(l, "data:") {
									continue
								}
								e := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(l, "data:")))
								check(t, e.Get("item"))
								if e.Get("type").String() == "response.failed" {
									terminals++
									if e.Get("response.status").String() != "failed" || len(e.Get("response.output").Array()) != 1 {
										t.Fatalf("failed response lost partial output: %s", e.Raw)
									}
								}
								for _, item := range e.Get("response.output").Array() {
									check(t, item)
								}
							}
						}
					}
					if terminals != 1 {
						t.Fatalf("terminal count = %d, want 1", terminals)
					}
				})
			})
		}
	}
}
