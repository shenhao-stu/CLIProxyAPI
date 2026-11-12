package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestAntigravityCarbonStalledReadClosesBody(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	body := &antigravityIdleStream{ReadCloser: reader, timeout: 15 * time.Millisecond}
	_, err := body.Read(make([]byte, 1))
	if !errors.Is(err, errAntigravityStreamIdle) {
		t.Fatalf("read returned %v", err)
	}
	status, ok := classifyAntigravityCarbonReadError(context.Background(), err).(statusErr)
	if !ok || status.StatusCode() != http.StatusGatewayTimeout || !status.IsStreamTruncated() {
		t.Fatalf("accepted stalled request should end with non-replayable 504: %#v", status)
	}
	if _, err := writer.Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("stalled body was not closed: %v", err)
	}
}

func TestAntigravityCarbonReadActivityPreservesBytes(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	go func() {
		defer writer.Close()
		_, _ = io.WriteString(writer, "data: native terminal\n")
	}()
	body := &antigravityIdleStream{ReadCloser: reader, timeout: time.Second}
	got, err := io.ReadAll(body)
	if err != nil || string(got) != "data: native terminal\n" {
		t.Fatalf("read = %q, %v", got, err)
	}
}
