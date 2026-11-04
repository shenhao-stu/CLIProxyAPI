package helps_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	responses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/responses"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/tidwall/gjson"
)

type boundaryPipeline struct {
	state     any
	progress  helps.GeminiResponsesProgress
	meter     helps.StreamUsageBuffer
	terminals []gjson.Result
}

func newBoundaryPipeline() *boundaryPipeline {
	return &boundaryPipeline{progress: helps.NewGeminiResponsesProgress("gemini-4-argon", sdktranslator.FormatOpenAIResponse)}
}

// This is the real executor's raw-accounting -> filter -> translator -> progress order.
// synthEOF is distinct from an actual upstream [DONE] frame.
func (p *boundaryPipeline) feed(raw string, synthEOF bool) []gjson.Result {
	var original []byte
	payload := []byte("[DONE]")
	if !synthEOF {
		original = []byte("data: " + raw)
		p.meter.ObserveGeminiStream(original)
		payload = helps.JSONPayload(helps.FilterSSEUsageMetadata(original))
	}
	chunks := responses.ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-4-argon", nil, nil, payload, &p.state)
	chunks = p.progress.Forward(original, chunks)
	var result []gjson.Result
	for _, c := range chunks {
		for _, line := range bytes.Split(c, []byte("\n")) {
			v := gjson.ParseBytes(helps.JSONPayload(line))
			switch v.Get("type").String() {
			case "response.completed", "response.failed", "response.incomplete":
				result = append(result, v)
				p.terminals = append(p.terminals, v)
			}
		}
	}
	return result
}

func boundarySummary(t *testing.T, p *boundaryPipeline) log.Fields {
	t.Helper()
	l := log.StandardLogger()
	oldOut, oldLevel := l.Out, l.Level
	oldHooks := l.ReplaceHooks(make(log.LevelHooks))
	l.SetOutput(io.Discard)
	l.SetLevel(log.InfoLevel)
	h := logtest.NewLocal(l)
	defer func() { l.ReplaceHooks(oldHooks); l.SetOutput(oldOut); l.SetLevel(oldLevel) }()
	p.progress.LogOutcome(context.Background(), nil)
	if h.LastEntry() == nil {
		t.Fatal("expected diagnostic summary")
	}
	return h.LastEntry().Data
}

func TestHistoricalBoundaryCleanEOFIsNotNativeFinish(t *testing.T) {
	p := newBoundaryPipeline()
	p.feed(`{"candidates":[{"content":{"parts":[{"thought":true,"text":"synthetic thought"}]}}],"usageMetadata":{"promptTokenCount":100,"thoughtsTokenCount":3,"cachedContentTokenCount":90,"totalTokenCount":103}}`, false)
	terminals := p.feed("", true)
	if len(terminals) != 1 {
		t.Fatalf("terminal count=%d", len(terminals))
	}
	if terminals[0].Get("response.usage.output_tokens").Exists() {
		t.Fatal("missing candidates counter was fabricated")
	}
	fields := boundarySummary(t, p)
	if fields["native_terminal_seen"] != false || fields["translated_terminal_seen"] != true || fields["native_usage_fields_seen"] != uint8(29) {
		t.Fatalf("summary=%v", fields)
	}
}

func TestHistoricalBoundaryEmptyBodyWithRealFinalHasMeter(t *testing.T) {
	p := newBoundaryPipeline()
	terminals := p.feed(`{"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":0,"totalTokenCount":100}}`, false)
	if len(terminals) != 1 || !terminals[0].Get("response.usage.output_tokens").Exists() || terminals[0].Get("response.usage.output_tokens").Int() != 0 {
		t.Fatal("valid explicit zero meter lost for empty output")
	}
	fields := boundarySummary(t, p)
	if fields["native_terminal_seen"] != true {
		t.Fatalf("summary=%v", fields)
	}
}

func TestHistoricalBoundaryUnspecifiedCannotConsumeRealFinal(t *testing.T) {
	p := newBoundaryPipeline()
	early := p.feed(`{"responseId":"synthetic-boundary","candidates":[{"content":{"parts":[{"text":"synthetic partial"}]},"finishReason":"FINISH_REASON_UNSPECIFIED"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":1,"totalTokenCount":101}}`, false)
	if len(early) != 0 {
		t.Errorf("nonterminal UNSPECIFIED emitted %d terminal(s) before real STOP", len(early))
	}
	late := p.feed(`{"candidates":[{"content":{"parts":[{"text":"synthetic tail"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":7,"totalTokenCount":107}}`, false)
	if len(late) != 1 || late[0].Get("response.usage.output_tokens").Int() != 7 {
		t.Errorf("real final downstream meter absent/wrong: late terminals=%d", len(late))
	}
	detail, ok := p.meter.Detail()
	if !ok || detail.OutputTokens != 7 {
		t.Errorf("independent raw meter failed: %+v ok=%v", detail, ok)
	}
}
