package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const geminiQuotaExhaustedFixture = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`

func sharedGeminiQuotaFixture(now time.Time) *Auth {
	return &Auth{ID: "gemini-shared-quota", Provider: "antigravity", Status: StatusActive,
		ModelStates: map[string]*ModelState{"gemini-3.8-flash-high": {
			Status: StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Hour),
			Quota:     QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Hour)},
			LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: geminiQuotaExhaustedFixture},
		}}}
}

func TestAntigravityGeminiSharedQuotaBoundary(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name, model string
		change      func(*Auth)
		want        bool
	}{
		{"same model", "gemini-3.8-flash-high", nil, true},
		{"other Gemini", "gemini-4-carbon-high", nil, true},
		{"Ultra prefix", "ultra/gemini-4-carbon-low", func(a *Auth) { a.Prefix = "ultra" }, true},
		{"new Gemini", "gemini-4-hydrogen-xhigh", nil, true},
		{"Claude independent", "claude-opus-5-5-high", nil, false},
		{"image Gemini shared", "gemini-3.1-flash-image", nil, true},
		{"other provider", "gemini-4-carbon-low", func(a *Auth) { a.Provider = "gemini" }, false},
		{"burst limit independent", "gemini-4-carbon-low", func(a *Auth) {
			a.ModelStates["gemini-3.8-flash-high"].LastError.Message = `{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"}]}}`
		}, false},
		{"unknown 429 independent", "gemini-4-carbon-low", func(a *Auth) { a.ModelStates["gemini-3.8-flash-high"].LastError.Message = "quota exhausted" }, false},
		{"expired resets", "gemini-4-carbon-low", func(a *Auth) {
			s := a.ModelStates["gemini-3.8-flash-high"]
			s.NextRetryAfter = now.Add(-time.Second)
			s.Quota.NextRecoverAt = now.Add(-time.Second)
		}, false},
		{"disabled protected", "claude-opus-5-5-high", func(a *Auth) { a.Disabled = true }, true},
		{"disabled Gemini protected", "gemini-4-carbon-low", func(a *Auth) { a.ModelStates["gemini-4-carbon-low"] = &ModelState{Status: StatusDisabled} }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := sharedGeminiQuotaFixture(now)
			if tc.change != nil {
				tc.change(a)
			}
			blocked, _, _ := isAuthBlockedForModel(a, tc.model, now)
			if blocked != tc.want {
				t.Fatalf("blocked=%v want=%v", blocked, tc.want)
			}
		})
	}
}

func TestAntigravityGeminiSharedQuotaKeepsStrongerBlocks(t *testing.T) {
	now := time.Now()
	a := sharedGeminiQuotaFixture(now)
	a.ModelStates["gemini-4-carbon-low"] = &ModelState{Unavailable: true, NextRetryAfter: now.Add(2 * time.Hour)}
	blocked, _, until := isAuthBlockedForModel(a, "gemini-4-carbon-low", now)
	if !blocked || !until.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("longer model block lost: %v %v", blocked, until)
	}
	a.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(3 * time.Hour)}
	for _, model := range []string{"gemini-4-hydrogen-xhigh", "claude-opus-5-5-high"} {
		blocked, _, until = isAuthBlockedForModel(a, model, now)
		if !blocked || !until.Equal(now.Add(3*time.Hour)) {
			t.Fatalf("credential block lost: %s %v %v", model, blocked, until)
		}
	}
}

func TestAntigravityGeminiQuotaRefreshesWarmScheduler(t *testing.T) {
	for _, prefix := range []string{"", "ultra/"} {
		t.Run(prefix, func(t *testing.T) { checkGeminiWarmScheduler(t, prefix) })
	}
}
func checkGeminiWarmScheduler(t *testing.T, prefix string) {
	now := time.Now()
	models := []string{"gemini-3.8-flash-high", "gemini-4-carbon-low", "claude-opus-5-5-high"}
	a := sharedGeminiQuotaFixture(now)
	a.Prefix = strings.TrimSuffix(prefix, "/")
	for i := range models {
		models[i] = prefix + models[i]
	}
	infos := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		infos = append(infos, &registry.ModelInfo{ID: model})
	}
	registry.GetGlobalRegistry().RegisterClient(a.ID, a.Provider, infos)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
	healthy := a.Clone()
	healthy.ModelStates = nil
	s := newSchedulerForTest(&RoundRobinSelector{}, healthy)
	pick := func(model string, want bool) {
		t.Helper()
		got, err := s.pickSingle(context.Background(), "antigravity", model, cliproxyexecutor.Options{}, nil)
		if (err == nil && got != nil) != want {
			t.Fatalf("model=%s selected=%v error=%v want=%v", model, got != nil, err, want)
		}
	}
	for _, model := range models {
		pick(model, true)
	}
	a.Generation = 1
	s.upsertAuthResult(a, []string{models[0]}, false)
	pick(models[1], false)
	pick(models[2], true)
	// A successful Claude turn cannot clear another family's persisted quota.
	a.Generation = 2
	a.ModelStates[models[2]] = &ModelState{Status: StatusActive}
	s.upsertAuthResult(a, []string{models[2]}, false)
	pick(models[1], false)
	a.Generation = 3
	a.ModelStates["gemini-3.8-flash-high"].NextRetryAfter = now.Add(-time.Second)
	a.ModelStates["gemini-3.8-flash-high"].Quota.NextRecoverAt = now.Add(-time.Second)
	s.upsertAuthResult(a, []string{models[0]}, false)
	pick(models[1], true)
}
