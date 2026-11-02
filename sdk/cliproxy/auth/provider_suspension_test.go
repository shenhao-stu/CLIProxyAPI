package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const suspensionBody = `{"error":{"code":403,"message":"This service has been disabled in this account for violation of Terms of Service.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION","domain":"cloudcode-pa.googleapis.com"}]}}`

func TestProviderSuspensionDisablesCredentialAndPersists(t *testing.T) {
	ctx := context.Background()
	store := newMemoryAuthTestStore()
	m := NewManager(store, nil, nil)
	if _, err := m.Register(ctx, &Auth{ID: "suspended-auth", Provider: "antigravity", Status: StatusActive, Metadata: map[string]any{"type": "antigravity", "disabled": false}}); err != nil {
		t.Fatal(err)
	}
	m.MarkResult(ctx, Result{AuthID: "suspended-auth", Provider: "antigravity", Model: "gemini-a", Error: &Error{HTTPStatus: http.StatusForbidden, Message: suspensionBody}})
	check := func() {
		t.Helper()
		a, _ := m.GetByID("suspended-auth")
		if !a.Disabled || a.Status != StatusDisabled {
			t.Fatalf("provider suspension not retained: disabled=%v status=%s unavailable=%v", a.Disabled, a.Status, a.Unavailable)
		}
		for _, model := range []string{"gemini-a", "gemini-b"} {
			blocked, _, _ := isAuthBlockedForModel(a, model, time.Now().Add(48*time.Hour))
			if !blocked {
				t.Fatalf("suspended credential selectable after cooldown for %s", model)
			}
		}
		stored, err := store.List(ctx)
		if err != nil || len(stored) != 1 || !stored[0].Disabled {
			t.Fatalf("suspension not persisted: %v", err)
		}
	}
	check()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.MarkResult(ctx, Result{AuthID: "suspended-auth", Provider: "antigravity", Model: "gemini-b", Success: true, CompletedResponse: true})
		}()
	}
	wg.Wait()
	check()
	a, _ := m.GetByID("suspended-auth")
	if a.InferenceVerified || !a.LastInferenceVerifiedAt.IsZero() {
		t.Fatal("late completed response verified a disabled credential")
	}
	if a.Success != 8 || a.Failed != 1 {
		t.Fatalf("real outcomes lost: success=%d failed=%d", a.Success, a.Failed)
	}
	executor := &mockOAuthErrorExecutor{id: "antigravity"}
	m.RegisterExecutor(executor)
	if _, err := m.refreshAuthForRequest(ctx, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	check()
	reloaded := NewManager(store, nil, nil)
	if err := reloaded.Load(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, _ := reloaded.GetByID(a.ID)
	if loaded == nil || !loaded.Disabled || loaded.Status != StatusDisabled {
		t.Fatal("restart restored suspended credential")
	}
}

func TestProviderSuspensionRemainingQuotaReturnsEarliestReset(t *testing.T) {
	const model = "suspension-quota-model"
	provider := "antigravity"
	reset := time.Now().Add(16 * time.Hour)
	auths := []*Auth{{ID: "suspended", Provider: provider, Disabled: true, Status: StatusDisabled}}
	for _, id := range []string{"quota-a", "quota-b", "quota-c"} {
		auths = append(auths, &Auth{ID: id, Provider: provider, Status: StatusError, ModelStates: map[string]*ModelState{model: {Status: StatusError, Unavailable: true, NextRetryAfter: reset, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: reset}}}})
		reset = reset.Add(time.Hour)
	}
	registerSchedulerModels(t, provider, model, "suspended", "quota-a", "quota-b", "quota-c")
	scheduler := newSchedulerForTest(&RoundRobinSelector{}, auths...)
	_, err := scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	var cooldown *modelCooldownError
	if !errors.As(err, &cooldown) {
		t.Fatalf("want quota cooldown, got %T: %v", err, err)
	}
	if cooldown.StatusCode() != 429 {
		t.Fatalf("status=%d", cooldown.StatusCode())
	}
	if cooldown.Headers().Get("Retry-After") == "" {
		t.Fatal("missing reset Retry-After")
	}
}

func TestProviderSuspensionDoesNotDisableUnrelatedFailures(t *testing.T) {
	for _, tc := range []struct {
		name, provider, body string
		status               int
	}{
		{"generic403", "antigravity", `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"permission denied"}}`, 403},
		{"wrong_domain", "antigravity", strings.ReplaceAll(suspensionBody, "cloudcode-pa.googleapis.com", "other.example"), 403},
		{"wrong_reason", "antigravity", strings.ReplaceAll(suspensionBody, "TOS_VIOLATION", "SERVICE_DISABLED"), 403},
		{"wrong_type", "antigravity", strings.ReplaceAll(suspensionBody, "google.rpc.ErrorInfo", "example.ErrorInfo"), 403},
		{"wrong_status", "antigravity", suspensionBody, 429},
		{"wrong_body_status", "antigravity", strings.ReplaceAll(suspensionBody, "PERMISSION_DENIED", "RESOURCE_EXHAUSTED"), 403},
		{"wrong_provider", "gemini", suspensionBody, 403},
		{"plain_phrase", "antigravity", "TOS_VIOLATION: disabled account", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			ctx := context.Background()
			if _, err := m.Register(ctx, &Auth{ID: tc.name, Provider: tc.provider}); err != nil {
				t.Fatal(err)
			}
			m.MarkResult(ctx, Result{AuthID: tc.name, Provider: tc.provider, Model: "model", Error: &Error{HTTPStatus: tc.status, Message: tc.body}})
			a, _ := m.GetByID(tc.name)
			if a.Disabled {
				t.Fatal("unrelated failure disabled credential")
			}
		})
	}
}

func TestProviderSuspensionIgnoresStaleRegistration(t *testing.T) {
	m := NewManager(nil, nil, nil)
	ctx := context.Background()
	a, err := m.Register(ctx, &Auth{ID: "replaced-auth", Provider: "antigravity"})
	if err != nil {
		t.Fatal(err)
	}
	oldEpoch := a.RegistrationEpoch
	if _, err = m.Register(ctx, &Auth{ID: a.ID, Provider: a.Provider}); err != nil {
		t.Fatal(err)
	}
	m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: "model", RegistrationEpoch: oldEpoch, Error: &Error{HTTPStatus: 403, Message: suspensionBody}})
	current, _ := m.GetByID(a.ID)
	if current.Disabled {
		t.Fatal("old registration disabled replacement")
	}
}
