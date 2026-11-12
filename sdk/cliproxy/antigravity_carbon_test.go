package cliproxy

import (
	"slices"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestAntigravityCarbonRequiresExactAccountEntitlement(t *testing.T) {
	const high = "gemini-4-carbon-high"
	const low = "gemini-4-carbon-low"
	const argon = "gemini-4-argon-high"
	models := registry.GetAntigravityModels()
	for _, tc := range []struct {
		name string
		ids  []string
		want []string
	}{
		{"ordinary", []string{argon, "gemini-4-carbon-disabled"}, []string{argon}},
		{"high only", []string{high}, []string{high}},
		{"low only", []string{low}, []string{low}},
		{"qualified ultra", []string{low, high, argon}, []string{high, low, argon}},
		{"unknown", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hints := antigravityModelCapabilityHints{}
			if tc.ids != nil {
				hints.ModelIDs = make(map[string]struct{}, len(tc.ids))
				for _, id := range tc.ids {
					hints.ModelIDs[id] = struct{}{}
				}
			}
			var ids []string
			for _, model := range filterAntigravityModels(models, hints) {
				ids = append(ids, model.ID)
				if model.ID == low && (model.Name != low || model.Thinking == nil) {
					t.Fatalf("low variant metadata incomplete: %+v", model)
				}
			}
			if !slices.Equal(ids, tc.want) {
				t.Fatalf("published IDs %v, want %v", ids, tc.want)
			}
		})
	}
}
