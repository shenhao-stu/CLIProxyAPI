package logging

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http/httptest"
	"testing"
)

func TestRelayIDIsServerOwnedAndSeparateFromBilling(t *testing.T) {
	s := gin.New()
	s.Use(GinLogrusLogger())
	var id string
	s.GET("/v1/models", func(c *gin.Context) { id = GetGinRequestID(c); c.Header("X-Request-ID", "vendor-id"); c.Status(200) })
	r := httptest.NewRequest("GET", "/v1/models", nil)
	r.Header.Set("X-CPA-Request-ID", "spoof")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if _, err := uuid.Parse(id); err != nil || w.Header().Get("X-CPA-Request-ID") != id || w.Header().Get("X-Request-ID") != "vendor-id" {
		t.Fatalf("missing server-owned diagnostic ID, headers %v", w.Header())
	}
}
