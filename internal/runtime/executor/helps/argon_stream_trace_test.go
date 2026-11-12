package helps

import (
	"testing"
	"time"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestArgonTraceRetainsOnlyEventTypesAndReportedOutput(t *testing.T) {
	tr := NewArgonStreamTrace("gemini-4-argon", sdktranslator.FormatClaude, time.Now())
	tr.Native()
	buf := []byte("data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"name\":\"private\"}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":12}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	tr.Forwarded(buf)
	for i := range buf {
		buf[i] = 0
	}
	if !tr.sawNative || !tr.tool || tr.event != "message_stop" || !tr.metered || tr.output != 12 {
		t.Fatal("wrong terminal trace")
	}
	tr.Forwarded([]byte("data: {\"type\":\"private\",\"message\":\"private\"}"))
	if tr.event != "message_stop" {
		t.Fatal("untrusted event entered diagnostics")
	}
}
