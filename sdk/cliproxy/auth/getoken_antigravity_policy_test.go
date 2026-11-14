package auth

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestGetokenAntigravityLocationPolicy(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	const locationMessage = "User location is not supported for the API use."
	const model = "gemini-3.8-flash-high"
	const otherModel = "gemini-pro-agent"
	const payload = `{"result":"ok"}`
	locationError := fmt.Sprintf(`{"error":{"code":400,"message":%q,"status":"FAILED_PRECONDITION"}}`, locationMessage)
	cases := []struct {
		name       string
		provider   string
		kind       string
		body       string
		configured bool
		failover   bool
	}{
		{"location", "antigravity", "oauth", locationError, true, true},
		{"unconfigured", "antigravity", "oauth", locationError, false, false},
		{"invalid_argument", "antigravity", "oauth", `{"error":{"status":"INVALID_ARGUMENT","message":"Invalid thinking level: minimal"}}`, true, false},
		{"other_precondition", "antigravity", "oauth", `{"error":{"status":"FAILED_PRECONDITION","message":"Request precondition failed"}}`, true, false},
		{"api_key", "antigravity", "apikey", locationError, true, false},
		{"other_provider", "xai", "oauth", locationError, true, false},
	}
	for _, test := range cases {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", test.name, streaming), func(t *testing.T) {
				cfg := &internalconfig.Config{}
				if test.configured {
					cfg.OAuthRequestScopedErrors = map[string][]internalconfig.RequestScopedErrorRule{
						"antigravity": {{
							Status: http.StatusBadRequest,
							Match:  []string{locationMessage},
							Action: RequestScopedActionContinueAndCooldown,
						}},
					}
				}
				manager := NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				reg := registry.GetGlobalRegistry()
				firstID, secondID := t.Name()+"/first", t.Name()+"/second"
				for i, id := range []string{firstID, secondID} {
					auth := &Auth{
						ID: id, Provider: test.provider, Status: StatusActive,
						Attributes: map[string]string{"auth_kind": test.kind, "priority": fmt.Sprint(10 - i)},
					}
					if test.kind == "apikey" {
						auth.Attributes["api_key"] = "test-key"
					}
					reg.RegisterClient(id, test.provider, []*registry.ModelInfo{{ID: model}, {ID: otherModel}})
					t.Cleanup(func() { reg.UnregisterClient(id) })
					if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
						t.Fatalf("register auth: %v", errRegister)
					}
				}
				var calls []string
				execute := func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					calls = append(calls, auth.ID)
					if auth.ID == firstID {
						return cliproxyexecutor.Response{}, customStatusError{code: http.StatusBadRequest, msg: test.body}
					}
					return cliproxyexecutor.Response{Payload: []byte(payload)}, nil
				}
				manager.RegisterExecutor(&customStreamMockExecutor{
					identifier:              test.provider,
					mockCustomErrorExecutor: mockCustomErrorExecutor{executeFn: execute},
					streamFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						response, errExecute := execute(ctx, auth, req, opts)
						chunks := make(chan cliproxyexecutor.StreamChunk, 1)
						chunks <- cliproxyexecutor.StreamChunk{Payload: response.Payload, Err: errExecute}
						close(chunks)
						return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
					},
				})
				request := cliproxyexecutor.Request{Model: model}
				var body []byte
				var errExecute error
				if streaming {
					var result *cliproxyexecutor.StreamResult
					result, errExecute = manager.ExecuteStream(context.Background(), []string{test.provider}, request, cliproxyexecutor.Options{})
					if errExecute == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								errExecute = chunk.Err
							}
							body = append(body, chunk.Payload...)
						}
					}
				} else {
					var response cliproxyexecutor.Response
					response, errExecute = manager.Execute(context.Background(), []string{test.provider}, request, cliproxyexecutor.Options{})
					body = response.Payload
				}
				wantCalls := []string{firstID}
				if test.failover {
					wantCalls = append(wantCalls, secondID)
					if errExecute != nil || string(body) != payload {
						t.Fatalf("failover response = %s, error = %v", body, errExecute)
					}
				} else if errExecute == nil || statusCodeFromError(errExecute) != http.StatusBadRequest {
					t.Fatalf("expected original 400, got %v", errExecute)
				}
				if !slices.Equal(calls, wantCalls) {
					t.Fatalf("credential calls = %v, want %v", calls, wantCalls)
				}
				first, ok := manager.GetByID(firstID)
				if !ok {
					t.Fatal("first auth missing after execution")
				}
				state := first.ModelStates[model]
				if test.failover {
					if state == nil || !state.Unavailable || !state.NextRetryAfter.After(time.Now()) {
						t.Fatalf("location failure must cool the affected model: %+v", state)
					}
				} else if first.Unavailable || !first.NextRetryAfter.IsZero() || (state != nil && (state.Unavailable || !state.NextRetryAfter.IsZero())) {
					t.Fatalf("unmatched request error must not cool auth/model: %+v", first)
				}
				if other := first.ModelStates[otherModel]; other != nil && (other.Unavailable || !other.NextRetryAfter.IsZero()) {
					t.Fatalf("location failure cooled unrelated model: %+v", other)
				}
			})
		}
	}
}
