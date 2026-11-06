package helps

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// NormalizeArgonSDKOpening translates the client runtime identity before user
// payload rules. Conversation history, tool data and later instructions retain
// their original text.
func NormalizeArgonSDKOpening(model string, doc *ojson.Doc) {
	switch model {
	case "gemini-4-argon", "gemini-4-argon-low", "gemini-4-argon-medium", "gemini-4-argon-high":
	default:
		return
	}
	if doc == nil {
		return
	}

	for _, path := range []string{"request.systemInstruction", "request.system_instruction"} {
		instruction := doc.Get(path)
		if instruction.Type == ojson.String {
			if text, ok := argonSDKOpening(instruction.String()); ok {
				_ = doc.Set(path, text)
			}
			continue
		}
		instruction.Get("parts").ForEach(func(index, part ojson.Result) bool {
			value := part.Get("text")
			if value.Type == ojson.String {
				if text, ok := argonSDKOpening(value.String()); ok {
					_ = doc.Set(path+".parts."+index.String()+".text", text)
				}
			}
			return true
		})
	}
}

func argonSDKOpening(text string) (string, bool) {
	for _, identity := range [...]struct{ from, to string }{
		{"You are a Claude agent, built on Anthropic's Claude Agent SDK.", "You are a coding agent."},
		{"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.", "You are Claude Code, running within the coding agent runtime."},
		{"You are a claude-sdk ", "You are a "},
	} {
		if len(text) >= len(identity.from) && strings.EqualFold(text[:len(identity.from)], identity.from) {
			return identity.to + text[len(identity.from):], true
		}
	}
	return text, false
}
