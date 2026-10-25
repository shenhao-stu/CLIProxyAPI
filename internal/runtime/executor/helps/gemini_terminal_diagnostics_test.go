package helps

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestGeminiEmptyTerminalDiagnostics(t *testing.T) {
	logger := log.StandardLogger()
	previous, level := logger.Out, logger.GetLevel()
	hooks := logger.ReplaceHooks(make(log.LevelHooks))
	logger.SetOutput(io.Discard)
	logger.SetLevel(log.InfoLevel)
	t.Cleanup(func() { logger.SetOutput(previous); logger.SetLevel(level); logger.ReplaceHooks(hooks) })
	hook := logtest.NewLocal(logger)
	for _, reason := range []string{"STOP", "MALFORMED_FUNCTION_CALL", "SAFETY", "private https://source.invalid/secret"} {
		t.Run(reason, func(t *testing.T) {
			hook.Reset()
			p := startedGeminiProgress(t)
			raw := []byte(`{"candidates":[{"finishReason":"` + reason + `"}],"usageMetadata":{"promptTokenCount":19,"candidatesTokenCount":0}}`)
			p.Forward(raw, [][]byte{[]byte(`data: {"type":"response.completed"}`)})
			p.LogOutcome(context.Background(), nil)
			entry := hook.LastEntry()
			if entry == nil {
				t.Fatal("empty upstream terminal has no diagnostic")
			}
			want := reason
			if strings.HasPrefix(reason, "private") {
				want = "unknown"
			}
			if entry.Data["native_finish_reason"] != want || entry.Data["native_text_seen"] != false || entry.Data["native_thought_seen"] != false || entry.Data["native_tool_seen"] != false {
				t.Fatalf("unexpected structural diagnostic: %v", entry.Data)
			}
			text, _ := entry.String()
			if strings.Contains(text, "source.invalid") || strings.Contains(text, "secret") {
				t.Fatal("upstream string leaked to diagnostics")
			}
		})
	}
}

func TestGeminiTerminalDiagnosticUsesProductionFormatter(t *testing.T) {
	logger := log.StandardLogger()
	previous, level, format := logger.Out, logger.GetLevel(), logger.Formatter
	hooks := logger.ReplaceHooks(make(log.LevelHooks))
	var output bytes.Buffer
	logger.SetOutput(&output)
	logger.SetLevel(log.InfoLevel)
	logger.SetFormatter(&logging.LogFormatter{})
	t.Cleanup(func() {
		logger.SetOutput(previous)
		logger.SetLevel(level)
		logger.SetFormatter(format)
		logger.ReplaceHooks(hooks)
	})
	p := startedGeminiProgress(t)
	p.Forward([]byte(`{"candidates":[{"content":{"parts":[{"text":"synthetic-private-thought","thought":true},{"text":"synthetic-private-answer"},{"functionCall":{"name":"synthetic-tool","args":{"private":"data"}}}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}`), [][]byte{[]byte(`data: {"type":"response.incomplete"}`)})
	p.LogOutcome(context.Background(), nil)
	for _, field := range []string{"native_finish_reason=MALFORMED_FUNCTION_CALL", "native_text_seen=true", "native_thought_seen=true", "native_tool_seen=true", "native_other_output_seen=false"} {
		if !strings.Contains(output.String(), field) {
			t.Errorf("missing field %s", field)
		}
	}
	if strings.Contains(output.String(), "private") || strings.Contains(output.String(), "synthetic-tool") {
		t.Fatal("diagnostic exposed output content")
	}
}
