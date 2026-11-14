package auth

import (
	"net/http"

	"github.com/MoeForge/moego/ojson"
)

func isProviderAccountSuspension(provider string, err *Error) bool {
	if provider != "antigravity" || err == nil || err.HTTPStatus != http.StatusForbidden {
		return false
	}
	if !ojson.Valid(err.Message) {
		return false
	}
	body := ojson.Parse(err.Message).Get("error")
	code := body.Get("code")
	if code.Type != ojson.Number || code.Float() != http.StatusForbidden || body.Get("status").String() != "PERMISSION_DENIED" || !body.Get("details").IsArray() {
		return false
	}
	for _, detail := range body.Get("details").Array() {
		if detail.Get("@type").String() == "type.googleapis.com/google.rpc.ErrorInfo" && detail.Get("domain").String() == "cloudcode-pa.googleapis.com" && detail.Get("reason").String() == "TOS_VIOLATION" {
			return true
		}
	}
	return false
}

func verificationResultError(provider string, err error) *Error {
	result := resultErrorFromError(err)
	if result == nil {
		return nil
	}
	if isProviderAccountSuspension(provider, result) {
		// Preserve only validated suspension evidence before diagnostics truncate it.
		result.Message = `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION","domain":"cloudcode-pa.googleapis.com"}]}}`
	} else {
		result.Message = sanitizeRenewalMessage(result.Message)
	}
	return result
}
