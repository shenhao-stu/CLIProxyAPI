package claude

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func argonEvents(state *any, frame string) string {
	chunks := ConvertAntigravityResponseToClaude(context.Background(), "gemini-4-argon-high", nil, []byte(`{"model":"gemini-4-argon-high"}`), []byte(frame), state)
	return string(bytes.Join(chunks, nil))
}

const argonPrimer = `{"response":{"candidates":[{"content":{"parts":[{"text":"Preparing"}]},"finishReason":"FINISH_REASON_UNSPECIFIED"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":2}}}`
const argonToolStop = `{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"id":"call-a","name":"lookup","args":{"x":1}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":18}}}`

func TestArgonToolTurnClosesOnRealMeter(t *testing.T) {
	var state any
	primer := argonEvents(&state, argonPrimer)
	if strings.Contains(primer, "event: message_delta") || strings.Contains(primer, "event: message_stop") {
		t.Fatal("provisional finish terminated the turn")
	}
	terminal := argonEvents(&state, argonToolStop)
	for _, want := range []string{"event: content_block_stop", `"stop_reason":"tool_use"`, `"output_tokens":7`, "event: message_stop"} {
		if !strings.Contains(terminal, want) {
			t.Fatalf("missing %s in %s", want, terminal)
		}
	}
	if tail := argonEvents(&state, "[DONE]"); tail != "" {
		t.Fatalf("duplicated terminal: %s", tail)
	}
}
func TestArgonWaitsForSeparateFinalUsage(t *testing.T) {
	var state any
	first := argonEvents(&state, `{"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":9}}}`)
	if strings.Contains(first, "event: message_delta") {
		t.Fatal("partial meter terminated the turn")
	}
	final := argonEvents(&state, `{"response":{"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":3,"totalTokenCount":12}}}`)
	if !strings.Contains(final, `"output_tokens":3`) || !strings.Contains(final, "event: message_stop") {
		t.Fatalf("missing final: %s", final)
	}
}
func TestArgonIncompleteMeterEndsWithError(t *testing.T) {
	var state any
	argonEvents(&state, `{"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":9}}}`)
	tail := argonEvents(&state, "[DONE]")
	if !strings.Contains(tail, "event: error") || strings.Contains(tail, "event: message_stop") || strings.Contains(tail, `"output_tokens":0`) {
		t.Fatalf("invented completion: %s", tail)
	}
}

func TestArgonExplicitZeroIsTerminalAndUnrelatedModelKeepsEOFContract(t *testing.T) {
	frame := `{"response":{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":0,"totalTokenCount":5}}}`
	var state any
	stream := argonEvents(&state, frame)
	if !strings.Contains(stream, `"output_tokens":0`) || !strings.Contains(stream, "event: message_stop") {
		t.Fatal(stream)
	}
	var other any
	chunks := ConvertAntigravityResponseToClaude(context.Background(), "claude-sonnet-5-5", nil, []byte(`{"model":"claude-sonnet-5-5"}`), []byte(frame), &other)
	if strings.Contains(string(bytes.Join(chunks, nil)), "event: message_stop") {
		t.Fatal("changed non-Argon completion")
	}
}
