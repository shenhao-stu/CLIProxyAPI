package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAntigravityEmptyBoundary(t *testing.T) {
	const primer = `data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"synthetic-opaque-signature"}]}}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":90,"totalTokenCount":100}}}` + "\n\n"
	for _, tc := range []struct {
		name, tail string
	}{
		{name: "opaque_only_clean_eof"},
		{name: "truncated_json_clean_eof", tail: `data: {"response":{"candidates":[{"content":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authID := "antigravity-empty-boundary-" + tc.name
			capture := &antigravityUsageCapture{authID: authID, records: make(chan usage.Record, 4)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{}) })

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, primer+tc.tail)
			}))
			defer server.Close()
			executor := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
			result, err := executor.ExecuteStream(context.Background(), &cliproxyauth.Auth{
				ID:         authID,
				Metadata:   map[string]any{"access_token": "synthetic-token", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "project_id": "synthetic-project"},
				Attributes: map[string]string{"base_url": server.URL},
			}, cliproxyexecutor.Request{Model: "gemini-4-argon", Payload: []byte(`{"model":"gemini-4-argon","input":"synthetic","stream":true}`)}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			var streamError error
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					streamError = chunk.Err
				}
				if len(chunk.Payload) != 0 {
					t.Error("empty upstream stream committed a client payload")
				}
			}
			if statusCodeFromExecutorError, ok := streamError.(interface{ StatusCode() int }); !ok || statusCodeFromExecutorError.StatusCode() != http.StatusBadGateway {
				t.Fatalf("stream error = %v, want pre-commit 502", streamError)
			}
			select {
			case record := <-capture.records:
				if !record.Failed || record.Detail.TotalTokens != 100 || record.Detail.CachedTokens != 90 || record.Detail.OutputTokens != 0 {
					t.Fatalf("failed usage = %+v, want observed 100 total and 90 cached with zero output", record)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for failure usage")
			}
		})
	}
}
