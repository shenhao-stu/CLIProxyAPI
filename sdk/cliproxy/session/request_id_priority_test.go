package session

import (
	"fmt"
	"net/http"
	"testing"
)

func TestPayloadSessionOutranksRequestCorrelationID(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"responses cache key", `{"model":"gemini-4-argon","prompt_cache_key":"warm-prefix","input":"hello"}`, "pck:warm-prefix"},
		{"responses conversation", `{"conversation":{"id":"conversation-a"},"input":"hello"}`, "conv:conversation-a"},
		{"chat session", `{"session_id":"session-a","messages":[{"role":"user","content":"hello"}]}`, "session:session-a"},
		{"chat metadata session", `{"metadata":{"session_id":"session-a"},"messages":[{"role":"user","content":"hello"}]}`, "session:session-a"},
		{"native gemini cache", `{"cachedContent":"cachedContents/a","contents":[]}`, "geminicache:cachedContents/a"},
		{"thread", `{"thread_id":"thread-a","input":"hello"}`, "thread:thread-a"},
		{"nested session", `{"request":{"session_id":"session-a","contents":[]}}`, "session:session-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for turn := 0; turn < 2; turn++ {
				headers := http.Header{}
				requestID := fmt.Sprintf("request-%d", turn)
				headers.Set("X-Client-Request-Id", requestID)
				got, ok := ExtractSessionInfo(headers, []byte(tc.body), nil)
				if !ok || got.SessionID != tc.want {
					t.Fatalf("turn %d identity=%q, want stable body identity %q", turn, got.SessionID, tc.want)
				}
				if headers.Get("X-Client-Request-Id") != requestID {
					t.Fatal("session extraction changed the request correlation ID")
				}
			}
		})
	}
}
