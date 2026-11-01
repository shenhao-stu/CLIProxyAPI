package auth

import (
	"encoding/json"
	"net/http"
)

func isProviderAccountSuspension(provider string, err *Error) bool {
	if provider != "antigravity" || err == nil || err.HTTPStatus != http.StatusForbidden {
		return false
	}
	var body struct {
		Error struct {
			Code    int    `json:"code"`
			Status  string `json:"status"`
			Details []struct {
				Type   string `json:"@type"`
				Reason string `json:"reason"`
				Domain string `json:"domain"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(err.Message), &body) != nil || body.Error.Code != http.StatusForbidden || body.Error.Status != "PERMISSION_DENIED" {
		return false
	}
	for _, detail := range body.Error.Details {
		if detail.Type == "type.googleapis.com/google.rpc.ErrorInfo" && detail.Domain == "cloudcode-pa.googleapis.com" && detail.Reason == "TOS_VIOLATION" {
			return true
		}
	}
	return false
}
