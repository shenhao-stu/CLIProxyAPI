package executor

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type antigravityOAuthCallerTransport struct{ calls, closes int }

func (r *antigravityOAuthCallerTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls++
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"synthetic-access","expires_in":3600}`))}, nil
}

func (r *antigravityOAuthCallerTransport) CloseIdleConnections() { r.closes++ }

func TestAntigravityOAuthRefreshDoesNotCloseCallerTransport(t *testing.T) {
	caller := &antigravityOAuthCallerTransport{}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", caller)
	_, err := (&AntigravityExecutor{}).refreshTokenSingleFlight(ctx, nil, "synthetic-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if caller.calls != 1 || caller.closes != 0 {
		t.Errorf("caller transport calls=%d closes=%d, want 1/0", caller.calls, caller.closes)
	}
}

func TestAntigravityOAuthRefreshNegotiatesProtocolWithoutChangingCCPA(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		name := "HTTP1 fallback"
		if h2 {
			name = "HTTP2"
		}
		t.Run(name, func(t *testing.T) {
			hellos := make(chan []string, 2)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/ccpa" {
					if r.ProtoMajor != 1 {
						t.Errorf("CCPA protocol = %s, want HTTP/1.1", r.Proto)
					}
					_, _ = io.WriteString(w, `{}`)
					return
				}
				wantProtocol, wantUA := 1, "Go-http-client/1.1"
				if h2 {
					wantProtocol, wantUA = 2, "Go-http-client/2.0"
				}
				if r.ProtoMajor != wantProtocol {
					t.Errorf("OAuth protocol = %s, want major %d", r.Proto, wantProtocol)
				}
				if r.UserAgent() != wantUA {
					t.Errorf("OAuth User-Agent = %q, want %q", r.UserAgent(), wantUA)
				}
				if r.Host != "oauth2.googleapis.com" {
					t.Errorf("OAuth Host = %q", r.Host)
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				wantForm := url.Values{"client_id": {antigravityClientID}, "client_secret": {antigravityClientSecret}, "grant_type": {"refresh_token"}, "refresh_token": {"synthetic-refresh"}}
				if !reflect.DeepEqual(r.PostForm, wantForm) {
					t.Error("OAuth refresh form differs from the native field contract")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"access_token":"synthetic-access","expires_in":3600}`)
			}))
			server.EnableHTTP2 = h2
			server.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
				hellos <- append([]string(nil), hello.SupportedProtos...)
				return nil, nil
			}}
			server.StartTLS()
			defer server.Close()
			base := &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"caller-private"}},
				TLSNextProto:    make(map[string]func(string, *tls.Conn) http.RoundTripper),
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
				},
			}
			defer base.CloseIdleConnections()
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", base)
			executor := &AntigravityExecutor{}
			auth := &cliproxyauth.Auth{ID: t.Name()}
			result, err := executor.refreshTokenSingleFlight(ctx, auth, "synthetic-refresh")
			if err != nil {
				t.Fatal(err)
			}
			if result.AccessToken != "synthetic-access" {
				t.Error("refresh result lost access token")
			}
			if got := <-hellos; !reflect.DeepEqual(got, []string{"h2", "http/1.1"}) {
				t.Errorf("OAuth advertised ALPN = %v, want [h2 http/1.1]", got)
			}
			if base.ForceAttemptHTTP2 || len(base.TLSNextProto) != 0 || !reflect.DeepEqual(base.TLSClientConfig.NextProtos, []string{"caller-private"}) {
				t.Error("OAuth refresh mutated caller transport")
			}
			ccpa := newAntigravityHTTPClient(ctx, nil, auth, 0)
			resp, err := ccpa.Get("https://oauth2.googleapis.com/ccpa")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			ccpa.CloseIdleConnections()
			if got := <-hellos; len(got) != 0 {
				t.Errorf("CCPA advertised ALPN = %v, want none", got)
			}
		})
	}
}

func TestAntigravityOAuthTransportPreservesProxyPriorityAndIsolation(t *testing.T) {
	for _, tc := range []struct{ name, request, auth, global, want string }{
		{"request override", "http://request.invalid:8081", "http://auth.invalid:8082", "http://global.invalid:8083", "http://request.invalid:8081"},
		{"auth proxy", "", "http://auth.invalid:8082", "http://global.invalid:8083", "http://auth.invalid:8082"},
		{"global proxy", "", "", "http://global.invalid:8083", "http://global.invalid:8083"},
		{"explicit direct", "", "direct", "http://global.invalid:8083", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := cliproxyexecutor.WithRequestProxyURL(context.Background(), tc.request)
			cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: tc.global}}
			auth := &cliproxyauth.Auth{ID: "synthetic-account-a", ProxyURL: tc.auth}
			first := newAntigravityOAuthHTTPClient(ctx, cfg, auth)
			second := newAntigravityOAuthHTTPClient(ctx, cfg, auth)
			defer first.CloseIdleConnections()
			defer second.CloseIdleConnections()
			transport := first.Transport.(*http.Transport)
			if first.Transport == second.Transport {
				t.Error("OAuth refreshes share a mutable transport")
			}
			request, _ := http.NewRequest(http.MethodPost, "https://oauth2.googleapis.com/token", nil)
			got := ""
			if transport.Proxy != nil {
				proxy, err := transport.Proxy(request)
				if err != nil {
					t.Fatal(err)
				}
				if proxy != nil {
					got = proxy.String()
				}
			}
			if got != tc.want {
				t.Errorf("proxy = %q, want %q", got, tc.want)
			}
		})
	}
}
