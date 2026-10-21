package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type getokenHelloRecordingConn struct {
	net.Conn
	read bytes.Buffer
}

func (c *getokenHelloRecordingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.read.Write(p[:n])
	return n, err
}

// TestGetokenAntigravityRawClientHello records the real managed transport using
// synthetic inference and a loopback TLS endpoint. Only certificate trust and,
// for direct mode, DNS routing differ from production. Optional evidence is raw
// TLS records, not a claim that parsed ClientHelloInfo proves native equivalence.
func TestGetokenAntigravityRawClientHello(t *testing.T) {
	const inferenceHostname = "daily-cloudcode-pa.googleapis.com"
	const oauthHostname = "oauth2.googleapis.com"
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic-offline-cpa"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{inferenceHostname, oauthHostname},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	parsedCertificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsedCertificate)
	var records []map[string]any
	for _, scenario := range []struct{ family, route, hostname string }{
		{"ccpa", "direct", inferenceHostname}, {"ccpa", "socks5", inferenceHostname},
		{"oauth", "direct", oauthHostname}, {"oauth", "socks5", oauthHostname},
	} {
		route, hostname := scenario.route, scenario.hostname
		t.Run(scenario.family+"/"+route, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			observed := make(chan map[string]any, 1)
			finished := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					finished <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				if route == "socks5" {
					if err := getokenSyntheticSOCKSConnect(conn, hostname); err != nil {
						finished <- err
						return
					}
				}
				raw := &getokenHelloRecordingConn{Conn: conn}
				var record map[string]any
				secure := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{certificate}, GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
					record = map[string]any{
						"kind": "client_hello", "implementation": "CPA Antigravity transport", "family": scenario.family, "route": route,
						"authority": hostname + ":443", "server_name": h.ServerName, "ciphers": h.CipherSuites,
						"curves": h.SupportedCurves, "signatures": h.SignatureSchemes, "alpn": h.SupportedProtos,
						"versions": h.SupportedVersions, "raw_records_b64": base64.StdEncoding.EncodeToString(raw.read.Bytes()),
						"go_version": runtime.Version(), "goos": runtime.GOOS, "goarch": runtime.GOARCH,
						"godebug": os.Getenv("GODEBUG"), "source_manifest": os.Getenv("CPA_SOURCE_MANIFEST"),
						"config": "production empty antigravity config; default short mode", "synthetic_only": true,
					}
					return nil, nil
				}})
				if err := secure.Handshake(); err != nil {
					finished <- err
					return
				}
				var plaintext bytes.Buffer
				request, err := http.ReadRequest(bufio.NewReader(io.TeeReader(secure, &plaintext)))
				if err != nil {
					finished <- err
					return
				}
				_, err = io.Copy(io.Discard, request.Body)
				if err != nil {
					finished <- err
					return
				}
				record["raw_http_b64"] = base64.StdEncoding.EncodeToString(plaintext.Bytes())
				record["http_protocol"] = request.Proto
				record["negotiated_alpn"] = secure.ConnectionState().NegotiatedProtocol
				observed <- record
				body := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`
				if scenario.family == "oauth" {
					body = `{"access_token":"synthetic-new-access","expires_in":3600}`
				}
				_, err = fmt.Fprintf(secure, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
				finished <- err
			}()
			proxyURL := ""
			if route == "socks5" {
				proxyURL = "socks5://" + listener.Addr().String()
			}
			auth := antigravityAuthWithIDAndProxy(t.Name(), proxyURL)
			auth.Attributes = map[string]string{"base_url": "https://" + hostname}
			cfg := &config.Config{}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client := newAntigravityHTTPClient(ctx, cfg, auth, 10*time.Second)
			if scenario.family == "oauth" {
				client = newAntigravityOAuthHTTPClient(ctx, cfg, auth)
				client.Timeout = 10 * time.Second
				defer client.CloseIdleConnections()
			}
			transport, ok := client.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("unexpected transport %T", client.Transport)
			}
			defer closeAntigravityAuthIdleTransports(auth)
			if transport.TLSClientConfig == nil {
				transport.TLSClientConfig = &tls.Config{}
			}
			transport.TLSClientConfig.RootCAs = roots
			if route == "direct" {
				transport.Proxy = nil
				transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
					if address != hostname+":443" {
						return nil, fmt.Errorf("unexpected synthetic endpoint %q", address)
					}
					return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
				}
			}
			if scenario.family == "ccpa" {
				executor := NewAntigravityExecutor(cfg)
				_, err = executor.Execute(ctx, auth,
					cliproxyexecutor.Request{Model: "gemini-3.8-flash-high", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"synthetic offline request"}]}]}`)},
					cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini})
			} else {
				var response *http.Response
				response, err = client.PostForm("https://"+hostname+"/token", url.Values{
					"client_id": {"synthetic-client"}, "client_secret": {"synthetic-secret"},
					"grant_type": {"refresh_token"}, "refresh_token": {"synthetic-refresh"},
				})
				if err == nil {
					_, err = io.Copy(io.Discard, response.Body)
					response.Body.Close()
					if response.StatusCode != http.StatusOK {
						t.Errorf("synthetic OAuth status %d", response.StatusCode)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			record := <-observed
			expectedALPN := []string(nil)
			if scenario.family == "oauth" {
				expectedALPN = []string{"h2", "http/1.1"}
			}
			if !reflect.DeepEqual(record["alpn"], expectedALPN) || record["http_protocol"] != "HTTP/1.1" || record["negotiated_alpn"] != "" {
				t.Fatal("Antigravity transport changed HTTP/ALPN behavior")
			}
			records = append(records, record)
		})
	}
	if path := os.Getenv("CPA_CLIENTHELLO_EVIDENCE"); path != "" && !t.Failed() {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		for _, record := range records {
			if err := json.NewEncoder(file).Encode(record); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func getokenSyntheticSOCKSConnect(conn net.Conn, hostname string) error {
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return err
	}
	if greeting[0] != 5 {
		return fmt.Errorf("unexpected SOCKS version %d", greeting[0])
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	if !bytes.Contains(methods, []byte{0}) {
		return fmt.Errorf("synthetic proxy requires no-auth method")
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return err
	}
	var command [5]byte
	if _, err := io.ReadFull(conn, command[:]); err != nil {
		return err
	}
	if command[0] != 5 || command[1] != 1 || command[2] != 0 || command[3] != 3 {
		return fmt.Errorf("unexpected synthetic SOCKS connect header")
	}
	address := make([]byte, int(command[4])+2)
	if _, err := io.ReadFull(conn, address); err != nil {
		return err
	}
	if string(address[:len(address)-2]) != hostname || binary.BigEndian.Uint16(address[len(address)-2:]) != 443 {
		return fmt.Errorf("unexpected synthetic SOCKS target")
	}
	_, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 1})
	return err
}
