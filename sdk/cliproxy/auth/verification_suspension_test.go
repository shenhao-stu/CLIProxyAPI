package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type suspensionVerifyExecutor struct {
	mockOAuthErrorExecutor
	failure *Error
	mode    string
}

func (e *suspensionVerifyExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, e.failure
}

func (e *suspensionVerifyExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	switch e.mode {
	case "nonstream":
		return nil, &Error{HTTPStatus: http.StatusNotImplemented}
	case "chunk":
		chunks := make(chan cliproxyexecutor.StreamChunk, 1)
		chunks <- cliproxyexecutor.StreamChunk{Err: e.failure}
		close(chunks)
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	default:
		return nil, e.failure
	}
}

func TestVerifyInferencePreservesSuspensionProofBeforeRedaction(t *testing.T) {
	// Provider details follow an arbitrarily long message, beyond diagnostic limits.
	body := strings.Replace(suspensionBody, "This service has been disabled in this account for violation of Terms of Service.", strings.Repeat("provider explanation ", 40)+"https://private.example/path?access_token=secret-value", 1)
	for _, mode := range []string{"stream", "chunk", "nonstream"} {
		for _, proven := range []bool{true, false} {
			name := mode + "/generic403"
			failureBody := strings.ReplaceAll(body, "cloudcode-pa.googleapis.com", "other.example")
			if proven {
				name = mode + "/suspended"
				failureBody = body
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				store := newMemoryAuthTestStore()
				m := NewManager(store, nil, nil)
				failure := &Error{HTTPStatus: http.StatusForbidden, Message: failureBody}
				m.RegisterExecutor(&suspensionVerifyExecutor{mockOAuthErrorExecutor: mockOAuthErrorExecutor{id: "antigravity"}, failure: failure, mode: mode})
				if _, err := m.Register(ctx, &Auth{ID: name, Provider: "antigravity", Metadata: map[string]any{"disabled": false}}); err != nil {
					t.Fatal(err)
				}
				after, err := m.VerifyInference(ctx, name, "gemini-4-argon-high")
				if err == nil || after == nil || after.Disabled != proven || after.InferenceVerified {
					t.Fatalf("disabled=%v expected=%v error=%v auth=%+v", after != nil && after.Disabled, proven, err, after)
				}
				stored, err := store.List(ctx)
				if err != nil || len(stored) != 1 || stored[0].Disabled != proven {
					t.Fatalf("suspension persistence failed: %v", err)
				}
				if failure.Message != failureBody {
					t.Fatal("verification mutated executor error")
				}
				if after.LastError == nil || strings.Contains(after.LastError.Message, "private.example") || strings.Contains(after.LastError.Message, "secret-value") {
					t.Fatalf("unsafe stored diagnostic: %+v", after.LastError)
				}
				if proven && !isProviderAccountSuspension("antigravity", after.LastError) {
					t.Fatal("stored diagnostic lost machine-readable suspension proof")
				}
			})
		}
	}
}
