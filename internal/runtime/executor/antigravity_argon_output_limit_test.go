package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAntigravityArgonPreservesExplicitOutputLimit(t *testing.T) {
	for _, model := range []string{"gemini-4-argon", "gemini-4-argon-low", "gemini-4-argon-medium", "gemini-4-argon-high"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", model, stream), func(t *testing.T) {
				payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"synthetic"}]}],"generationConfig":{"maxOutputTokens":512,"thinkingConfig":{"includeThoughts":false}}}}`)
				auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "test"}}
				req, err := (&AntigravityExecutor{}).buildRequest(context.Background(), auth, "test", model, payload, stream, "", "https://example.test")
				if err != nil {
					t.Fatal(err)
				}
				wire, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				if got := ojson.GetBytes(wire, "request.generationConfig.maxOutputTokens"); got.Int() != 512 {
					t.Fatalf("explicit output limit lost: %s", got.Raw)
				}
				if got := ojson.GetBytes(wire, "request.generationConfig.thinkingConfig.includeThoughts"); got.Type != ojson.False {
					t.Fatalf("explicit summary preference changed: %s", got.Raw)
				}
			})
		}
	}
}

func TestAntigravityArgonOutputLimitAcrossProtocols(t *testing.T) {
	for _, tc := range []struct {
		format sdktranslator.Format
		body   string
	}{
		{sdktranslator.FormatOpenAI, `{"messages":[{"role":"user","content":"synthetic"}],"max_tokens":512}`},
		{sdktranslator.FormatOpenAIResponse, `{"input":"synthetic","max_output_tokens":512}`},
		{sdktranslator.FormatClaude, `{"messages":[{"role":"user","content":"synthetic"}],"max_tokens":512}`},
		{sdktranslator.FormatGemini, `{"contents":[{"role":"user","parts":[{"text":"synthetic"}]}],"generationConfig":{"maxOutputTokens":512}}`},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.format, stream), func(t *testing.T) {
				observed := make(chan []byte, 1)
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
					}
					observed <- body
					const response = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":512,"totalTokenCount":513}}}`
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: "+response+"\n\n")
					} else {
						_, _ = io.WriteString(w, response)
					}
				}))
				defer server.Close()
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", server.Client().Transport)
				auth := antigravityAuthWithIDAndProxy(t.Name(), "")
				auth.Attributes = map[string]string{"base_url": server.URL}
				e := NewAntigravityExecutor(&config.Config{})
				req := cliproxyexecutor.Request{Model: "gemini-4-argon-high", Payload: []byte(tc.body)}
				opts := cliproxyexecutor.Options{SourceFormat: tc.format, ResponseFormat: tc.format, Stream: stream}
				if stream {
					result, errExecute := e.ExecuteStream(ctx, auth, req, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else {
					if _, errExecute := e.Execute(ctx, auth, req, opts); errExecute != nil {
						t.Fatal(errExecute)
					}
				}
				if got := ojson.GetBytes(<-observed, "request.generationConfig.maxOutputTokens"); got.Int() != 512 {
					t.Fatalf("wire output limit = %s", got.Raw)
				}
			})
		}
	}
}

func TestAntigravityOutputLimitDoesNotAddDefaultsOrChangeOtherModels(t *testing.T) {
	for _, tc := range []struct {
		model  string
		config string
		want   string
	}{
		{"gemini-4-argon-high", `{}`, ""},
		{"gemini-3-pro-high", `{"maxOutputTokens":512}`, ""},
		{"gemini-3-pro-image", `{"maxOutputTokens":512}`, ""},
		{"claude-sonnet-5-5", `{"maxOutputTokens":512}`, "512"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"synthetic"}]}],"generationConfig":` + tc.config + `}}`)
			auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "test"}}
			req, err := (&AntigravityExecutor{}).buildRequest(context.Background(), auth, "test", tc.model, payload, false, "", "https://example.test")
			if err != nil {
				t.Fatal(err)
			}
			wire, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if got := ojson.GetBytes(wire, "request.generationConfig.maxOutputTokens").Raw; got != tc.want {
				t.Fatalf("output limit = %q, want %q", got, tc.want)
			}
		})
	}
}
