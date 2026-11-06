package helps

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestArgonSDKOpening(t *testing.T) {
	const original = `{"request":{"systemInstruction":{"parts":[{"text":"You are a claude-sdk coding subagent. Keep claude-sdk in the examples."},{"text":"Install claude-sdk."}]},"contents":[{"role":"user","parts":[{"text":"claude-sdk"},{"functionResponse":{"name":"claude-sdk","response":{"result":"claude-sdk"}}}]}],"tools":[{"functionDeclarations":[{"name":"claude-sdk"}]}]}}`
	doc := ojson.NewDocString(original)
	NormalizeArgonSDKOpening("gemini-4-argon-high", doc)
	if got := doc.Get("request.systemInstruction.parts.0.text").String(); got != "You are a coding subagent. Keep claude-sdk in the examples." {
		t.Fatalf("opening = %q", got)
	}
	for _, path := range []string{"request.systemInstruction.parts.1", "request.contents", "request.tools"} {
		if doc.Get(path).Raw != ojson.Get(original, path).Raw {
			t.Fatal("modified", path)
		}
	}
	before := doc.String()
	NormalizeArgonSDKOpening("gemini-4-argon-high", doc)
	if doc.String() != before {
		t.Fatal("second application changed payload")
	}
	for _, model := range []string{"gemini-3.8-flash-high", "claude-opus-5-5", "gemini-4-argon-image"} {
		doc := ojson.NewDocString(original)
		NormalizeArgonSDKOpening(model, doc)
		if doc.String() != original {
			t.Fatal("changed unrelated model", model)
		}
	}
}

func TestArgonKnownClientHeaders(t *testing.T) {
	tests := map[string]string{
		"You are a Claude agent, built on Anthropic's Claude Agent SDK.":                                 "You are a coding agent.",
		"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.": "You are Claude Code, running within the coding agent runtime.",
		"Use the claude-sdk to build a sample.":                                                          "Use the claude-sdk to build a sample.",
		"You are a coding assistant. Debug claude-sdk.":                                                  "You are a coding assistant. Debug claude-sdk.",
	}
	for original, want := range tests {
		doc := ojson.NewDocString(`{}`)
		_ = doc.Set("request.system_instruction.parts.0.text", original)
		NormalizeArgonSDKOpening("gemini-4-argon", doc)
		if got := doc.Get("request.system_instruction.parts.0.text").String(); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}
