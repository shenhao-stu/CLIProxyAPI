package auth

import (
	"net/http"

	"github.com/MoeForge/moego/ojson"
)

// A verified capacity rejection must let a warm replacement keep the session.
// Generic 5xx and transport failures retain the existing transient policy.
func isModelCapacityResultError(err *Error) bool {
	if err == nil || err.HTTPStatus != http.StatusServiceUnavailable || !ojson.Valid(err.Message) {
		return false
	}
	body := ojson.Parse(err.Message).Get("error")
	code := body.Get("code")
	if code.Type != ojson.Number || code.Float() != http.StatusServiceUnavailable {
		return false
	}
	if body.Get("status").String() != "UNAVAILABLE" || !body.Get("details").IsArray() {
		return false
	}
	for _, detail := range body.Get("details").Array() {
		if detail.Get("@type").String() == "type.googleapis.com/google.rpc.ErrorInfo" &&
			detail.Get("domain").String() == "cloudcode-pa.googleapis.com" &&
			detail.Get("reason").String() == "MODEL_CAPACITY_EXHAUSTED" {
			return true
		}
	}
	return false
}
