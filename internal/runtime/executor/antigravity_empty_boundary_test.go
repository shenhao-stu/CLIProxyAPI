package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAntigravityEmptyBoundary(t *testing.T) {
	const primer = `data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"synthetic-opaque-signature"}]}}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":90,"totalTokenCount":100}}}` + "\n\n"
	for _, tc := range []struct {
		name, tail string
		wantError  bool
	}{
		{name: "opaque_only_clean_eof"},
		{name: "truncated_json_clean_eof", tail: `data: {"response":{"candidates":[{"content":`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, primer+tc.tail)
			}))
			defer server.Close()
			executor := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
			result, err := executor.ExecuteStream(context.Background(), &cliproxyauth.Auth{
				Metadata:   map[string]any{"access_token": "synthetic-token", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "project_id": "synthetic-project"},
				Attributes: map[string]string{"base_url": server.URL},
			}, cliproxyexecutor.Request{Model: "gemini-4-argon", Payload: []byte(`{"model":"gemini-4-argon","input":"synthetic","stream":true}`)}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			var streamError error
			var failed bool
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					streamError = chunk.Err
				}
				for _, line := range bytes.Split(chunk.Payload, []byte("\n")) {
					line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
					event := ojson.ParseBytes(line)
					switch event.Get("type").String() {
					case "response.completed":
						t.Error("upstream produced no answer but translated completed")
					case "response.failed":
						failed = true
						if event.Get("response.usage.output_tokens").Exists() {
							t.Error("missing native output meter became a numeric value")
						}
						if event.Get("response.usage.input_tokens").Int() != 100 {
							t.Error("observed input meter was lost")
						}
					}
				}
			}
			if tc.wantError && streamError == nil {
				t.Error("clean HTTP EOF silently discarded an incomplete JSON frame")
			}
			if !tc.wantError && (streamError != nil || !failed) {
				t.Errorf("expected an explicit failed response, error=%v failed=%t", streamError, failed)
			}
		})
	}
}
