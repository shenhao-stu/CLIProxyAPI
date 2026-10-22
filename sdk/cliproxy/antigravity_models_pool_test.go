package cliproxy

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityNewPoolRefreshesCatalogAndFailureBackoff(t *testing.T) {
	for _, initialStatus := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(initialStatus), func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)
			overrideAntigravityModelUserAgent(t)
			var calls atomic.Int32
			previous := http.DefaultTransport
			http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
				if calls.Add(1) == 1 {
					return antigravityVersionTestResponse(req, initialStatus, `{"models":{"old":{}}}`), nil
				}
				return antigravityVersionTestResponse(req, http.StatusOK, `{"models":{"new":{}}}`), nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			svc := &Service{cfg: &config.Config{}}
			auth := antigravityTestAuth("pool-change", "https://pool.invalid")
			svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
			if initialStatus == http.StatusOK {
				auth.Metadata["access_token"] = "rotated-fixture-token"
				svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
				if calls.Load() != 1 {
					t.Fatal("token rotation invalidated a successful catalog")
				}
			}
			auth.Prefix = "ultra"
			hints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
			if _, ok := hints.ModelIDs["new"]; !ok || calls.Load() != 2 {
				t.Fatalf("new pool inherited old catalog/backoff: models=%v requests=%d", hints.ModelIDs, calls.Load())
			}
			svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
			if calls.Load() != 2 {
				t.Fatal("unchanged pool did not reuse refreshed catalog")
			}
		})
	}
}

func TestAntigravityNewPoolRetainsPersistedFallbackOnFailedProbe(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	overrideAntigravityModelUserAgent(t)
	svc := &Service{cfg: &config.Config{}}
	auth := antigravityTestAuth("pool-fallback", "https://pool.invalid")
	auth.Metadata[antigravityPersistedHintsMetaKey] = buildPersistedAntigravityCapabilityHintsValue(
		"project-pool-fallback", "https://pool.invalid", antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{"old": {}}})
	var calls atomic.Int32
	previous := http.DefaultTransport
	http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
		status := http.StatusServiceUnavailable
		if calls.Add(1) == 1 {
			status = http.StatusOK
		}
		return antigravityVersionTestResponse(req, status, `{"models":{"old":{}}}`), nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	auth.Prefix = "ultra"
	hints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	if _, ok := hints.ModelIDs["old"]; !ok || calls.Load() != 2 {
		t.Fatalf("new pool lost fallback or skipped probe: models=%v requests=%d", hints.ModelIDs, calls.Load())
	}
}
