package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"testing/synctest"
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

func TestAntigravityCarbonReasoningReadGap(t *testing.T) {
	for _, tc := range []struct {
		name      string
		thinking  bool
		remaining time.Duration
		wantStop  bool
	}{
		{"real reasoning resumes", true, 8 * time.Minute, true},
		{"output phase keeps normal timeout", false, 8 * time.Minute, false},
		{"reasoning window is bounded", true, 180 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				reader, writer := io.Pipe()
				defer reader.Close()
				done := make(chan struct{})
				defer close(done)
				go func() {
					defer writer.Close()
					select {
					case <-time.After(358 * time.Second):
						_, _ = io.WriteString(writer, "STOP")
					case <-done:
					}
				}()
				body := &antigravityIdleStream{ReadCloser: reader,
					timeout: antigravityCarbonReadBudget(antigravityCarbonReadIdleTimeout, tc.thinking, tc.remaining)}
				n, err := body.Read(make([]byte, 16))
				if tc.wantStop && (n != 4 || err != nil) {
					t.Fatalf("resumed native data was lost: read %d, error %v", n, err)
				}
				if !tc.wantStop && !errors.Is(err, errAntigravityStreamIdle) {
					t.Fatalf("expected bounded idle failure, got %v", err)
				}
			})
		})
	}
}
