package responses

import (
	"context"
	"fmt"
	"github.com/tidwall/gjson"
	"strings"
	"testing"
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
							t.Errorf("message status %q is outside Responses message schema", got)
						}
					}
				}
				t.Run("buffered", func(t *testing.T) {
					final := gjson.ParseBytes(ConvertAntigravityResponseToOpenAIResponsesNonStream(context.Background(), "gemini-4-argon", request, nil, raw, nil))
					for _, item := range final.Get("output").Array() {
						check(t, item)
					}
				})
				t.Run("stream", func(t *testing.T) {
					var state any
					for _, r := range [][]byte{raw, []byte("[DONE]")} {
						for _, c := range ConvertAntigravityResponseToOpenAIResponses(context.Background(), "gemini-4-argon", request, nil, r, &state) {
							for _, l := range strings.Split(string(c), "\n") {
								if !strings.HasPrefix(l, "data:") {
									continue
								}
								e := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(l, "data:")))
								check(t, e.Get("item"))
								for _, item := range e.Get("response.output").Array() {
									check(t, item)
								}
							}
						}
					}
				})
			})
		}
	}
}
