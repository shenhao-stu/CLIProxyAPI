package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const progressThought = `{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"synthetic thought"}]}}]}`

func progressSSE(provider, raw string) string {
	if provider == "antigravity" {
		raw = `{"response":` + raw + `}`
	}
	return "data: " + raw + "\n\n"
}

func executeProgressStream(t *testing.T, ctx context.Context, provider, baseURL string) *cliproxyexecutor.StreamResult {
	t.Helper()
	auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"base_url": baseURL, "api_key": "test-key"}, Metadata: map[string]any{
		"access_token": "test-token", "project_id": "test-project", "expired": time.Now().Add(time.Hour).Format(time.RFC3339),
	}}
	auth.Prefix = "ultra"
	model := cliproxyauth.NewManager(nil, nil, nil).ResolveExecutionModel(auth, "ultra/gemini-3.8-flash-high")
	if model != "gemini-3.8-flash-high" {
		t.Fatalf("credential prefix was not removed before execution: %q", model)
	}
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"gemini-3.8-flash-high","input":"synthetic probe","stream":true}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: true}
	var result *cliproxyexecutor.StreamResult
	var err error
	if provider == "antigravity" {
		result, err = NewAntigravityExecutor(&config.Config{}).ExecuteStream(ctx, auth, req, opts)
	} else {
		result, err = NewGeminiExecutor(&config.Config{}).ExecuteStream(ctx, auth, req, opts)
	}
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	return result
}

func nextProgressChunk(t *testing.T, chunks <-chan cliproxyexecutor.StreamChunk) []byte {
	t.Helper()
	select {
	case chunk, ok := <-chunks:
		if !ok || chunk.Err != nil {
			t.Fatalf("unexpected stream end/error: open=%v error=%v", ok, chunk.Err)
		}
		return chunk.Payload
	case <-time.After(3 * time.Second):
		t.Fatal("received native progress but no downstream activity before upstream EOF")
		return nil
	}
}

func TestGeminiResponsesProgressBeforeEOF(t *testing.T) {
	for _, provider := range []string{"antigravity", "gemini"} {
		for _, activity := range []string{"buffered_thought", "native_comment", "multiline_thought"} {
			if provider == "gemini" && activity == "multiline_thought" {
				continue
			}
			t.Run(provider+"/"+activity, func(t *testing.T) {
				advance := make(chan struct{})
				upstreamCanceled := make(chan struct{})
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					defer close(upstreamCanceled)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, progressSSE(provider, progressThought))
					w.(http.Flusher).Flush()
					select {
					case <-r.Context().Done():
						return
					case <-advance:
					}
					line := ": private source detail https://synthetic.invalid/private\n\n"
					if activity == "buffered_thought" {
						line = progressSSE(provider, progressThought)
					} else if activity == "multiline_thought" {
						line = "data: {\n\"response\":" + progressThought + "\n}\n\n"
					}
					_, _ = io.WriteString(w, line)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				}))
				defer server.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				result := executeProgressStream(t, ctx, provider, server.URL)
				for range 2 {
					payload := nextProgressChunk(t, result.Chunks)
					if !strings.Contains(string(payload), "response.created") && !strings.Contains(string(payload), "response.in_progress") {
						t.Fatalf("expected bootstrap event, got %s", payload)
					}
				}
				select {
				case chunk := <-result.Chunks:
					t.Fatalf("silent upstream generated unsolicited activity: %s", chunk.Payload)
				default:
				}
				close(advance)
				payload := string(nextProgressChunk(t, result.Chunks))
				if payload != ": upstream-activity\n\n" {
					t.Fatalf("expected constant comment without private content or invented data, got %q", payload)
				}
				cancel()
				select {
				case <-upstreamCanceled:
				case <-time.After(3 * time.Second):
					t.Fatal("cancellation did not reach upstream")
				}
				if got := calls.Load(); got != 1 {
					t.Fatalf("upstream attempts=%d, want 1", got)
				}
			})
		}
	}
}

func TestGeminiResponsesProgressKeepsTerminalUsage(t *testing.T) {
	const finish = `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{},"traceId":"progress-test"}`
	const usage = `{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":2,"totalTokenCount":17},"traceId":"progress-test"}`
	for _, provider := range []string{"antigravity", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, frame := range []string{progressThought, progressThought, finish, usage} {
					_, _ = io.WriteString(w, progressSSE(provider, frame))
				}
			}))
			defer server.Close()
			result := executeProgressStream(t, context.Background(), provider, server.URL)
			comments, terminals := 0, 0
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				if string(chunk.Payload) == ": upstream-activity\n\n" {
					comments++
				}
				for _, line := range strings.Split(string(chunk.Payload), "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					root := gjson.Parse(strings.TrimPrefix(line, "data: "))
					if root.Get("type").String() == "response.completed" {
						terminals++
						if got := root.Get("response.usage"); got.Get("input_tokens").Int() != 10 || got.Get("output_tokens").Int() != 7 || got.Get("total_tokens").Int() != 17 {
							t.Fatalf("late authoritative usage changed: %s", got.Raw)
						}
					}
				}
			}
			if comments != 1 || terminals != 1 {
				t.Fatalf("comments=%d terminals=%d, want one each", comments, terminals)
			}
		})
	}
}
