package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAntigravityCarbonEOFAfterTextRequiresCompletionEvidence(t *testing.T) {
	const textPrefix = `data: {"response":{"responseId":"resp_fixture","candidates":[{"content":{"parts":[{"text":"partial answer"}]}}]`
	const plainText = textPrefix + "}}\n\n"
	const partialMeter = `,"usageMetadata":{"promptTokenCount":100}`
	const completeMeter = `,"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":5,"totalTokenCount":105}`
	const completeTail = "data: {\"response\":{\"responseId\":\"resp_fixture\"" + completeMeter + "}}\n\n"
	const secondText = "data: {\"response\":{\"responseId\":\"resp_fixture\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\" more\"}]}}]}}\n\n"
	const stop = "data: {\"response\":{\"responseId\":\"resp_fixture\",\"candidates\":[{\"finishReason\":\"STOP\"}]}}\n\n"
	const toolCall = `data: {"response":{"responseId":"resp_fixture","candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","id":"fixture_call","args":{"item":"test"}}}]}}]}}` + "\n\n"
	for _, tc := range []struct {
		name, wire      string
		wantInterrupted bool
	}{
		{"text-only EOF", plainText, true},
		{"input-only meter", textPrefix + partialMeter + "}}\n\n", true},
		{"zero output placeholder", textPrefix + `,"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":0}}}` + "\n\n", true},
		{"stale meter before more text", textPrefix + completeMeter + "}}\n\n" + secondText, true},
		{"native stop without final meter", plainText + stop, false},
		{"native DONE without final meter", plainText + "data: [DONE]\n\n", false},
		{"complete meter tail", plainText + completeTail, false},
		{"complete meter with last content", textPrefix + completeMeter + "}}\n\n", false},
		{"text and complete tool call", plainText + toolCall, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.wire)
			}))
			defer upstream.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			executor := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
			result, err := executor.ExecuteStream(ctx, &cliproxyauth.Auth{
				Prefix:     "ultra",
				Metadata:   map[string]any{"access_token": "synthetic", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "project_id": "fixture"},
				Attributes: map[string]string{"base_url": upstream.URL},
			}, cliproxyexecutor.Request{Model: "gemini-4-carbon", Payload: []byte(`{"model":"gemini-4-carbon","input":"test","stream":true}`)},
				cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			var failures []error
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					failures = append(failures, chunk.Err)
				} else {
					out.Write(chunk.Payload)
				}
			}
			if attempts.Load() != 1 || !bytes.Contains(out.Bytes(), []byte(`"delta":"partial answer"`)) {
				t.Fatalf("attempts=%d stream=%s", attempts.Load(), &out)
			}
			complete := bytes.Count(out.Bytes(), []byte("event: response.completed"))
			if !tc.wantInterrupted {
				if len(failures) != 0 || complete != 1 {
					t.Fatalf("complete=%d failures=%v stream=%s", complete, failures, &out)
				}
				return
			}
			if complete != 0 || len(failures) != 1 {
				t.Fatalf("complete=%d failures=%v stream=%s", complete, failures, &out)
			}
			var interrupted statusErr
			if !errors.As(failures[0], &interrupted) || interrupted.StatusCode() != 502 || !interrupted.IsStreamTruncated() {
				t.Fatalf("stream failure=%v", failures[0])
			}
		})
	}
}
