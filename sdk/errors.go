package kagi

import (
	"encoding/json"
	"fmt"
	"strings"
)

// APIError is a typed error describing a non-2xx response from the Kagi API.
// It carries the HTTP status, the backend wire error code (e.g. "KGI_TEN_004")
// parsed from the CustomResponse envelope's error.code, and a human-readable
// message. Callers classify failures with errors.As(&APIError{}) and branch on
// Status or Code rather than matching substrings of the error text.
type APIError struct {
	Status  int
	Code    string
	Message string
}

// Error implements the error interface.
func (e *APIError) Error() string {
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("kagi: API error %s (status %d): %s", e.Code, e.Status, e.Message)
	case e.Message != "":
		return fmt.Sprintf("kagi: API error (status %d): %s", e.Status, e.Message)
	case e.Code != "":
		return fmt.Sprintf("kagi: API error %s (status %d)", e.Code, e.Status)
	default:
		return fmt.Sprintf("kagi: API returned status %d", e.Status)
	}
}

// errorEnvelope is the subset of the backend CustomResponse envelope
// (success, message, data, pagination, error) needed to build an APIError: the
// top-level message and the nested error.code wire code (plus its optional
// message as a fallback).
type errorEnvelope struct {
	Message string `json:"message"`
	Error   struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// newAPIError builds an *APIError from a non-2xx response's status and body. It
// parses the backend envelope to surface error.code and the top-level message,
// falling back to the nested error message and then the raw body when the
// envelope is absent or unparseable.
func newAPIError(status int, body []byte) *APIError {
	apiErr := &APIError{Status: status}

	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err == nil {
		apiErr.Code = env.Error.Code
		apiErr.Message = env.Message
		if apiErr.Message == "" {
			apiErr.Message = env.Error.Message
		}
	}

	if apiErr.Message == "" {
		apiErr.Message = strings.TrimSpace(string(body))
	}

	return apiErr
}
