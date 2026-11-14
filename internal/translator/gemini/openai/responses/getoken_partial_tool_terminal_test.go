package responses

import (
	"context"
	"fmt"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// New upstream partialArgs tool search must retain the local terminal and metering contract.
func TestPartialToolSearchPreservesTerminalAndLateUsage(t *testing.T) {
	for _, tc := range []struct{ finish, status, itemStatus, errorCode string }{
		{"STOP", "completed", "completed", ""},
		{"MALFORMED_FUNCTION_CALL", "failed", "incomplete", "upstream_malformed_function_call"},
	} {
		t.Run(tc.finish, func(t *testing.T) {
			var state any
			var events []ojson.Result
			consume := func(raw string) {
				for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-4-argon", []byte(toolSearchFixtureRequest), nil, []byte(raw), &state) {
					_, event := parseSSEEvent(t, chunk)
					events = append(events, event)
				}
			}
			consume(`data: {"responseId":"partial-search-meter","candidates":[{"content":{"parts":[{"functionCall":{"name":"tool_search","partialArgs":[{"jsonPath":"$.query","stringValue":"synthetic"}],"willContinue":true}}]}}]}`)
			consume(fmt.Sprintf(`data: {"candidates":[{"content":{"parts":[{"functionCall":{"willContinue":false}}]},"finishReason":%q}]}`, tc.finish))
			for _, event := range events {
				if event.Get("type").String() == "response.completed" {
					t.Fatal("completed before the authoritative usage tail")
				}
				if event.Get("type").String() == "response.output_item.done" && event.Get("item.type").String() == "tool_search_call" && event.Get("item.status").String() != tc.itemStatus {
					t.Fatalf("tool item status=%s, want %s", event.Get("item.status").String(), tc.itemStatus)
				}
			}
			consume(`data: {"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":3,"thoughtsTokenCount":2,"totalTokenCount":105,"cachedContentTokenCount":90}}`)
			consume("[DONE]")
			terminal := issue6258Terminal(t, events)
			if terminal.Get("status").String() != tc.status || terminal.Get("error.code").String() != tc.errorCode {
				t.Fatalf("terminal status/error=%s/%s", terminal.Get("status").String(), terminal.Get("error.code").String())
			}
			issue6258AssertUsage(t, terminal, 100, 5, 2, 105, 90)
			if terminal.Get("output.0.arguments.query").String() != "synthetic" {
				t.Fatal("partial tool arguments lost")
			}
		})
	}
}
