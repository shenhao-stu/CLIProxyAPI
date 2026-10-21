package executor

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestGetokenAntigravitySerializedHeaderBoundary(t *testing.T) {
	for _, route := range []struct{ name, path string }{
		{"generate", antigravityGeneratePath},
		{"stream", antigravityStreamPath},
		{"count", antigravityCountTokensPath},
		{"raw", antigravityGeneratePath},
	} {
		t.Run(route.name, func(t *testing.T) {
			type observation struct {
				header, trailer                  http.Header
				host, path, protocol, negotiated string
				requestID                        string
			}
			observed := make(chan observation, 1)
			clientHello := make(chan []string, 1)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				observed <- observation{r.Header.Clone(), r.Trailer.Clone(), r.Host, r.URL.Path, r.Proto, r.TLS.NegotiatedProtocol, gjson.GetBytes(body, "requestId").String()}
				const response = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`
				w.Header().Set("Content-Type", "application/json")
				switch route.name {
				case "stream":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+response+"\n\n")
				case "count":
					_, _ = io.WriteString(w, `{"totalTokens":2}`)
				default:
					_, _ = io.WriteString(w, response)
				}
			}))
			server.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
				clientHello <- append([]string(nil), hello.SupportedProtos...)
				return nil, nil
			}}
			server.StartTLS()
			defer server.Close()
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", server.Client().Transport)
			auth := antigravityAuthWithIDAndProxy(t.Name(), "")
			auth.Attributes = map[string]string{"base_url": server.URL}
			exec := NewAntigravityExecutor(&config.Config{})
			headers := http.Header{
				"Authorization": {"Bearer downstream-private"}, "Proxy-Authorization": {"Basic downstream-private"},
				"Cookie": {"session=downstream-private"}, "X-Downstream-Private": {"downstream-private"},
				"Connection": {"close"}, "User-Agent": {"downstream-private"}, "Content-Type": {"application/json"},
			}
			before := headers.Clone()
			payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
			req := cliproxyexecutor.Request{Model: "gemini-3.8-flash-high", Payload: payload}
			opts := cliproxyexecutor.Options{Headers: headers, SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini}
			var err error
			switch route.name {
			case "generate":
				_, err = exec.Execute(ctx, auth, req, opts)
			case "stream":
				var result *cliproxyexecutor.StreamResult
				result, err = exec.ExecuteStream(ctx, auth, req, opts)
				if err == nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							err = chunk.Err
						}
					}
				}
			case "count":
				_, err = exec.CountTokens(ctx, auth, req, opts)
			case "raw":
				raw, errRequest := http.NewRequest(http.MethodPost, server.URL+route.path, strings.NewReader(string(payload)))
				if errRequest != nil {
					t.Fatal(errRequest)
				}
				raw.Header, raw.Close = headers, true
				raw.Host = "downstream-private.invalid"
				raw.ContentLength = -1
				raw.Trailer = http.Header{"X-Downstream-Private": {"downstream-private"}}
				var response *http.Response
				response, err = exec.HttpRequest(ctx, auth, raw)
				if raw.Host != "downstream-private.invalid" || raw.Trailer.Get("X-Downstream-Private") != "downstream-private" || !raw.Close {
					t.Error("caller request fields changed")
				}
				if err == nil {
					_, err = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(headers, before) {
				t.Fatal("caller headers changed")
			}
			got := <-observed
			if route.name == "generate" || route.name == "stream" {
				kind, id, _ := strings.Cut(got.requestID, "/")
				parsed, parseErr := uuid.Parse(id)
				if kind != "agent" || parseErr != nil || parsed.Version() != 4 {
					t.Errorf("request ID does not match native no-trajectory fallback: %q", got.requestID)
				}
			}
			if got.protocol != "HTTP/1.1" || got.negotiated != "" || len(<-clientHello) != 0 {
				t.Fatalf("unexpected HTTP/ALPN: %s %q", got.protocol, got.negotiated)
			}
			endpoint, _ := url.Parse(server.URL)
			if got.host != endpoint.Host || got.path != route.path {
				t.Errorf("unexpected target: %s %s", got.host, got.path)
			}
			if got.header.Get("Authorization") != "Bearer test-access-token" || got.header.Get("User-Agent") != resolveUserAgent(auth) || got.header.Get("Content-Type") != "application/json" {
				t.Fatalf("upstream credential/profile mismatch: %v", got.header)
			}
			for _, name := range []string{"Connection", "Proxy-Authorization", "Cookie", "X-Downstream-Private"} {
				if got.header.Get(name) != "" {
					t.Errorf("downstream header escaped: %s", name)
				}
			}
			if len(got.trailer) != 0 {
				t.Errorf("downstream trailers escaped: %v", got.trailer)
			}
			for _, values := range got.header {
				for _, value := range values {
					if strings.Contains(value, "downstream-private") {
						t.Errorf("downstream value escaped: %q", value)
					}
				}
			}
		})
	}
}
