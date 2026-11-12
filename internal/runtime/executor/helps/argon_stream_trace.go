package helps

import (
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// ArgonStreamTrace retains only stream metadata. A forwarded event records the
// executor handoff; downstream receipt and usage records establish settlement.
type ArgonStreamTrace struct {
	format          sdktranslator.Format
	start           time.Time
	first           time.Duration
	sawNative, tool bool
	event           string
	output          int64
	metered         bool
}

func NewArgonStreamTrace(model string, format sdktranslator.Format, start time.Time) *ArgonStreamTrace {
	if !util.IsGeminiArgon(model) || (format != sdktranslator.FormatClaude && format != sdktranslator.FormatOpenAIResponse) {
		return nil
	}
	return &ArgonStreamTrace{format: format, start: start}
}
func (t *ArgonStreamTrace) Native() {
	if t != nil && !t.sawNative {
		t.first = time.Since(t.start)
		t.sawNative = true
	}
}
func (t *ArgonStreamTrace) Forwarded(chunk []byte) {
	if t == nil {
		return
	}
	for len(chunk) > 0 {
		var line []byte
		line, chunk, _ = bytes.Cut(chunk, []byte("\n"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(line[5:]), []byte("[DONE]")) {
			t.event = "[DONE]"
			continue
		}
		p := JSONPayload(line)
		if len(p) == 0 {
			continue
		}
		root := util.ParseGJSONBytesNoCopy(p)
		kind := root.Get("type").String()
		var count ojson.Result
		switch kind {
		case "content_block_start":
			t.tool = t.tool || root.Get("content_block.type").String() == "tool_use"
		case "response.output_item.added":
			t.tool = t.tool || root.Get("item.type").String() == "function_call"
		case "message_delta":
			count = root.Get("usage.output_tokens")
		case "response.completed", "response.incomplete", "response.failed":
			count = root.Get("response.usage.output_tokens")
		case "message_start", "content_block_stop", "content_block_delta", "message_stop", "error", "response.created", "response.in_progress", "response.output_item.done", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.output_text.delta", "response.output_text.done":
		default:
			continue
		}
		if count.Type == ojson.Number && count.Int() >= 0 {
			t.metered = true
			t.output = count.Int()
		}
		t.event = strings.Clone(kind)
	}
}
func (t *ArgonStreamTrace) Log(ctx context.Context) {
	if t == nil {
		return
	}
	fields := log.Fields{"cpa_stream_id": logging.GetRequestID(ctx), "stream_format": t.format.String(), "last_stream_event": t.event, "stream_tool_seen": t.tool}
	if t.sawNative {
		fields["first_native_ms"] = t.first.Milliseconds()
	}
	if t.metered {
		fields["wire_output_tokens"] = t.output
	}
	LogWithRequestID(ctx).WithFields(fields).Info("argon stream delivery summary")
}
