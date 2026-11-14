package helps_test

import (
	"testing"
)

func TestEmptyBoundaryMetadataContainsOnlyShape(t *testing.T) {
	p := newBoundaryPipeline()
	p.feed(`{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"private-signature-must-not-be-logged"}]}}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":90,"totalTokenCount":100}}`, false)
	p.feed("", true)
	p.progress.IncompleteFrame(42)
	fields := boundarySummary(t, p)
	if fields["native_frame_count"] != uint64(1) || fields["native_part_count"] != uint64(1) || fields["native_signature_seen"] != true || fields["native_empty_text_seen"] != true || fields["incomplete_frame_bytes"] != 42 {
		t.Fatalf("shape metadata=%v", fields)
	}
	for _, value := range fields {
		if text, ok := value.(string); ok && text == "private-signature-must-not-be-logged" {
			t.Fatal("signature escaped into diagnostic fields")
		}
	}
}
