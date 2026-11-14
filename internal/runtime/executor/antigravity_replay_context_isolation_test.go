package executor

import (
	"bytes"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// A text match is not proof that encrypted reasoning belongs to this history.
func TestThoughtSignatureReplayRequiresMatchingHistory(t *testing.T) {
	original := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"tenant A instructions"}]},"contents":[{"role":"user","parts":[{"text":"environment A"}]},{"role":"model","parts":[{"text":"OK"}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	kind, fingerprint := antigravityReplayPartFingerprint(ojson.Parse(`{"text":"OK"}`))
	item := buildAntigravityThoughtSignatureItem(1, 0, "synthetic-encrypted-reasoning-A", kind, fingerprint)
	item = antigravityReplayItemContextHashForTest(item, original, 1)
	for _, path := range []string{"request.contents.0.parts.0.text", "request.systemInstruction.parts.0.text"} {
		t.Run(path, func(t *testing.T) {
			doc := ojson.NewDoc(original)
			if errSet := doc.Set(path, "different tenant context"); errSet != nil {
				t.Fatal(errSet)
			}
			changed := doc.Bytes()
			for _, engine := range []struct {
				name  string
				apply func([]byte, [][]byte, map[string]any) ([]byte, bool)
			}{
				{"batch", applyAntigravityReasoningReplayItems}, {"sequential", func(p []byte, i [][]byte, schemas map[string]any) ([]byte, bool) {
					return applyAntigravityReasoningReplayItemsSequential(newAntigravityReplayRequestIndex(p), p, i, schemas)
				}},
			} {
				t.Run(engine.name, func(t *testing.T) {
					out, _ := engine.apply(changed, [][]byte{item}, nil)
					if sig := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); sig != "" {
						t.Fatal("cached reasoning from another history was attached")
					}
					if !bytes.Equal(out, changed) {
						t.Fatal("rejected cache replay changed request semantics")
					}
				})
			}
		})
	}
	out, _ := applyAntigravityReasoningReplayItems(original, [][]byte{item}, nil)
	if ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String() != "synthetic-encrypted-reasoning-A" {
		t.Fatal("same-history replay stopped working")
	}
}

// Client-carried native state is not a server cache replay and stays untouched.
func TestThoughtSignatureContextGuardPreservesClientState(t *testing.T) {
	original := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"original"}]},{"role":"model","parts":[{"text":"OK"}]}]}}`)
	kind, fingerprint := antigravityReplayPartFingerprint(ojson.Parse(`{"text":"OK"}`))
	item := antigravityReplayItemContextHashForTest(buildAntigravityThoughtSignatureItem(1, 0, "cached-old-state", kind, fingerprint), original, 1)
	doc := ojson.NewDoc(original)
	if errSet := doc.Set("request.contents.0.parts.0.text", "changed"); errSet != nil {
		t.Fatal(errSet)
	}
	if errSet := doc.Set("request.contents.1.parts.0.thoughtSignature", "client-carried-native-state"); errSet != nil {
		t.Fatal(errSet)
	}
	changed := doc.Bytes()
	out, applied := applyAntigravityReasoningReplayItems(changed, [][]byte{item}, nil)
	if applied || !bytes.Equal(out, changed) {
		t.Fatal("context guard changed client-carried state")
	}
}
