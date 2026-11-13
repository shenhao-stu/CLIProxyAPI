package auth

import (
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
)

func isAntigravityGeminiModel(auth *Auth, model string) bool {
	if auth == nil || auth.Provider != "antigravity" {
		return false
	}
	key := canonicalModelLookupKey(model)
	if prefix := strings.ToLower(strings.TrimSpace(auth.Prefix)); prefix != "" {
		key = strings.TrimPrefix(key, prefix+"/")
	}
	return strings.HasPrefix(key, "gemini-")
}

// Antigravity Gemini models share a quota bucket; Claude has its own bucket.
// Only an explicit exhaustion response spreads the existing reset deadline.
// Burst limits and ambiguous 429s remain scoped to the reported model.
func antigravityGeminiQuotaDeadline(auth *Auth, now time.Time) time.Time {
	var until time.Time
	if auth == nil || auth.Provider != "antigravity" {
		return until
	}
	for model, state := range auth.ModelStates {
		if !isAntigravityGeminiModel(auth, model) || state == nil ||
			!state.Quota.Exceeded || state.Quota.Reason != "quota" || state.LastError == nil ||
			state.LastError.HTTPStatus != http.StatusTooManyRequests {
			continue
		}
		blocked, _, next := availabilityBlock(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now)
		if !blocked || !next.After(now) || !next.After(until) {
			continue
		}
		err := ojson.Get(state.LastError.Message, "error")
		if err.Get("status").String() != "RESOURCE_EXHAUSTED" {
			continue
		}
		for _, detail := range err.Get("details").Array() {
			if detail.Get("@type").String() == "type.googleapis.com/google.rpc.ErrorInfo" && detail.Get("reason").String() == "QUOTA_EXHAUSTED" {
				until = next
				break
			}
		}
	}
	return until
}
