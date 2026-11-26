package pushhttp

import (
	"encoding/json"
	"net/http"
)

// Error codes. Stable across releases; the messages beside them are not.
//
// A client that branches on these must keep working when the wording changes,
// which is the whole reason the code exists as a separate field. Adding one is
// a contract change and belongs in contract/openapi.yaml first.
const (
	codeUnauthenticated = "unauthenticated"
	codeMalformedBody   = "malformed_body"
	codeMissingField    = "missing_field"
	codeMalformedField  = "malformed_field"
	codeUnknownCategory = "unknown_category"
	codeNotSwitchable   = "category_not_switchable"
	codeInternal        = "internal"
)

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (a *API) fail(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Code: code, Message: message})
}
