package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

func TestGeminiResponsesProgressHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, beforeDataError := range []bool{false, true} {
		name := "progress_and_late_usage"
		if beforeDataError {
			name = "pre_data_comment_keeps_error_status"
		}
		t.Run(name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, ": private-origin-before-data\n\n")
				if beforeDataError {
					_, _ = io.WriteString(w, "data: {\"error\":{\"code\":400,\"message\":\"synthetic invalid argument\",\"status\":\"INVALID_ARGUMENT\"}}\n\n")
					return
				}
				for _, frame := range []string{
					`{"candidates":[{"content":{"parts":[{"thought":true,"text":"synthetic first"}]}}]}`,
					`{"candidates":[{"content":{"parts":[{"thought":true,"text":"synthetic second"}]}}]}`,
					`{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{},"traceId":"handler-progress"}`,
					`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":2,"totalTokenCount":17},"traceId":"handler-progress"}`,
				} {
					_, _ = io.WriteString(w, "data: {\"response\":"+frame+"}\n\n")
				}
			}))
			defer server.Close()
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor.NewAntigravityExecutor(&config.Config{}))
			const publicModel = "ultra/gemini-3.8-flash-high"
			auth := &coreauth.Auth{ID: t.Name(), Provider: "antigravity", Prefix: "ultra", Status: coreauth.StatusActive,
				Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{
					"access_token": "test-token", "project_id": "test-project", "expired": time.Now().Add(time.Hour).Format(time.RFC3339),
				}}
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: publicModel}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
			router := gin.New()
			router.POST("/v1/responses", handler.Responses)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+publicModel+`","input":"synthetic probe","stream":true}`))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			body := recorder.Body.String()
			if attempts.Load() != 1 {
				t.Fatalf("upstream attempts=%d, want 1", attempts.Load())
			}
			if strings.Contains(body, "private-origin") {
				t.Fatal("raw native comment escaped")
			}
			if beforeDataError {
				if recorder.Code != http.StatusBadRequest || strings.Contains(body, ": upstream-activity") {
					t.Fatalf("pre-data comment changed error status/body: %d %s", recorder.Code, body)
				}
				return
			}
			if recorder.Code != http.StatusOK || strings.Count(body, ": upstream-activity\n\n") != 1 {
				t.Fatalf("handler lost or duplicated progress: status=%d body=%s", recorder.Code, body)
			}
			terminals := 0
			for _, line := range strings.Split(body, "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				root := gjson.Parse(strings.TrimPrefix(line, "data: "))
				if root.Get("type").String() == "response.completed" {
					terminals++
					if usage := root.Get("response.usage"); usage.Get("input_tokens").Int() != 10 || usage.Get("output_tokens").Int() != 7 || usage.Get("total_tokens").Int() != 17 {
						t.Fatalf("handler changed late usage: %s", usage.Raw)
					}
				}
			}
			if terminals != 1 {
				t.Fatalf("terminal count=%d, want 1", terminals)
			}
		})
	}
}
