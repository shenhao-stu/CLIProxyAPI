package responses

import (
	"context"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestGeminiNonStreamOpaqueIsNotAnswer(t *testing.T) {
	for _, tc := range []struct {
		name, parts, status string
	}{
		{"empty", `[]`, "failed"},
		{"signature", `[{"text":"","thoughtSignature":"synthetic-opaque"}]`, "failed"},
		{"whitespace", `[{"text":"  \n"}]`, "failed"},
		{"thought_signature", `[{"thought":true,"text":"","thoughtSignature":"synthetic-opaque"}]`, "failed"},
		{"text", `[{"text":"synthetic answer"}]`, "completed"},
		{"thought", `[{"thought":true,"text":"synthetic reasoning"}]`, "completed"},
		{"tool", `[{"functionCall":{"name":"read_file","args":{"path":"synthetic.txt"},"id":"synthetic-call"}}]`, "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"candidates":[{"content":{"parts":` + tc.parts + `},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":90,"totalTokenCount":100}}`)
			out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-4-argon", nil, nil, raw, nil)
			if got := ojson.GetBytes(out, "status").String(); got != tc.status {
				t.Fatalf("status=%s want=%s", got, tc.status)
			}
			if ojson.GetBytes(out, "usage.output_tokens").Exists() || ojson.GetBytes(out, "usage.input_tokens").Int() != 100 {
				t.Fatal("partial upstream meter was fabricated or lost")
			}
		})
	}
}
