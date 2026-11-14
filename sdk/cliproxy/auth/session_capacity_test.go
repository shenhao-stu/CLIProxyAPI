package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const capacityErrorBody = `{"error":{"code":503,"status":"UNAVAILABLE","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","domain":"cloudcode-pa.googleapis.com","reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`

func TestCapacityFailoverKeepsWarmReplacement(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "inferred"
		if explicit {
			name = "explicit"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			const provider, model = "antigravity", "gemini-4-argon-high"
			m := NewManager(nil, nil, nil)
			s := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &RoundRobinSelector{}, TTL: time.Hour, RebindOn: SessionAffinityRebindOnQuota})
			defer s.Stop()
			m.SetSelector(s)
			m.RegisterExecutor(schedulerTestExecutor{provider: provider})
			for i, id := range []string{"capacity-old", "capacity-new"} {
				a := &Auth{ID: id, Provider: provider, Status: StatusActive, Attributes: map[string]string{"priority": []string{"1", "0"}[i]}}
				if _, err := m.Register(WithSkipPersist(ctx), a); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			opts := func() cliproxyexecutor.Options {
				o := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"synthetic persistent prefix"}]}`), Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "capacity-test"}}
				if explicit {
					o.Headers = http.Header{"Session-Id": []string{"capacity-session"}}
				}
				return o
			}
			pick := func(o cliproxyexecutor.Options, attempted map[string]struct{}, want string) {
				t.Helper()
				a, _, err := m.pickNext(ctx, provider, model, o, attempted)
				if err != nil || a == nil || a.ID != want {
					t.Fatalf("pick=%v err=%v want=%s", a, err, want)
				}
			}
			first := opts()
			pick(first, nil, "capacity-old")
			m.MarkResult(ctx, Result{AuthID: "capacity-old", Provider: provider, Model: model, Success: true, Options: first})
			failed := opts()
			pick(failed, nil, "capacity-old")
			m.MarkResult(ctx, Result{AuthID: "capacity-old", Provider: provider, Model: model, Error: &Error{HTTPStatus: 503, Message: capacityErrorBody}, Options: failed})
			replacement := opts()
			pick(replacement, map[string]struct{}{"capacity-old": {}}, "capacity-new")
			m.MarkResult(ctx, Result{AuthID: "capacity-new", Provider: provider, Model: model, Success: true, Options: replacement})
			expireSessionAffinityPriorityModelCooldown(t, m, "capacity-old", model)
			pick(opts(), nil, "capacity-new")
		})
	}
}

func TestCapacityRebindRequiresStructuredProviderEvidence(t *testing.T) {
	if !shouldRebindSessionAffinityOnError(&Error{HTTPStatus: 503, Message: capacityErrorBody}) {
		t.Fatal("verified capacity rejection must permit rebinding")
	}
	for _, e := range []*Error{
		{HTTPStatus: 500, Message: capacityErrorBody},
		{HTTPStatus: 503, Message: capacityErrorBody, Code: ErrorCodeRequestScoped},
		{HTTPStatus: 503, Message: capacityErrorBody, Code: ErrorCodeTransientTransport},
	} {
		if shouldRebindSessionAffinityOnError(e) {
			t.Fatal("request/transport attribution must take precedence")
		}
	}
	for name, body := range map[string]string{
		"generic":   `{"error":{"code":503,"status":"UNAVAILABLE","message":"MODEL_CAPACITY_EXHAUSTED"}}`,
		"domain":    strings.ReplaceAll(capacityErrorBody, "cloudcode-pa.googleapis.com", "unrelated.example"),
		"type":      strings.ReplaceAll(capacityErrorBody, "google.rpc.ErrorInfo", "unrelated.ErrorInfo"),
		"status":    strings.ReplaceAll(capacityErrorBody, "UNAVAILABLE", "INTERNAL"),
		"code":      strings.ReplaceAll(capacityErrorBody, "503", "500"),
		"malformed": capacityErrorBody[:len(capacityErrorBody)-1],
	} {
		t.Run(name, func(t *testing.T) {
			if shouldRebindSessionAffinityOnError(&Error{HTTPStatus: 503, Message: body}) {
				t.Fatal("unverified capacity must preserve affinity")
			}
		})
	}
}
