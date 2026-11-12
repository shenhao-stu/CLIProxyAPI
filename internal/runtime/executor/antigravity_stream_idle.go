package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Finish upstream silence before the downstream gateway's 180 second deadline.
const antigravityCarbonReadIdleTimeout = 150 * time.Second

var errAntigravityStreamIdle = errors.New("Antigravity upstream stream read timed out")

func antigravityCarbonReadTimeout(auth *cliproxyauth.Auth, model string, format sdktranslator.Format) time.Duration {
	if auth != nil && auth.Prefix == "ultra" && format == sdktranslator.FormatOpenAIResponse && util.IsGeminiCarbon(model) {
		return antigravityCarbonReadIdleTimeout
	}
	return 0
}

// Bound each blocking upstream read. Time spent delivering a frame downstream
// does not consume the upstream's budget.
type antigravityIdleStream struct {
	io.ReadCloser
	timeout time.Duration
}

func (s *antigravityIdleStream) Read(p []byte) (int, error) {
	if s.timeout == 0 {
		return s.ReadCloser.Read(p)
	}
	timedOut := make(chan struct{})
	timer := time.AfterFunc(s.timeout, func() {
		defer close(timedOut)
		_ = s.ReadCloser.Close()
	})
	n, err := s.ReadCloser.Read(p)
	if !timer.Stop() {
		<-timedOut
		return n, errAntigravityStreamIdle
	}
	return n, err
}

func antigravityCarbonIdleError() statusErr {
	return statusErr{
		code:            http.StatusGatewayTimeout,
		msg:             `{"error":{"type":"server_error","code":"upstream_stream_idle_timeout","message":"The upstream stopped sending stream data before completion","status_code":504}}`,
		streamTruncated: true,
	}
}

func classifyAntigravityCarbonReadError(ctx context.Context, err error) error {
	if !errors.Is(err, errAntigravityStreamIdle) {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return antigravityCarbonIdleError()
}
