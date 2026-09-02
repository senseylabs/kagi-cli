package kagi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Backend wire error codes the CLI branches on. They are compared, never
// printed: the human-facing wording lives with the command that renders it.
const (
	// ErrCodeAccountNotOnboarded (KGI_SEC_038) is the refusal every route
	// except the onboarding escape hatches returns while an account has not
	// finished onboarding. It is a state of the caller's own account, not a
	// permission problem, and must never be presented as "access denied" or be
	// answered with a re-login: the session is valid, the account is not ready.
	ErrCodeAccountNotOnboarded = "KGI_SEC_038"
	// ErrCodeDomainClaimedByOtherOrg (KGI_STA_002) refuses creating an own
	// organization because the caller's verified email domain is already
	// claimed by another organization. The way forward is joining that
	// organization, never retrying the create.
	ErrCodeDomainClaimedByOtherOrg = "KGI_STA_002"
)

// HasErrorCode reports whether err is (or wraps) an APIError carrying the given
// backend wire code. Callers branch on this rather than on the HTTP status,
// which is shared by unrelated refusals.
func HasErrorCode(err error, code string) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

// IsNotOnboarded reports whether err is the backend's not-onboarded refusal
// (KGI_SEC_038). It is a definitive answer about the caller's own account, so a
// command may act on it — clear the session, explain the situation — rather
// than treating it as a transient failure.
func IsNotOnboarded(err error) bool {
	return HasErrorCode(err, ErrCodeAccountNotOnboarded)
}

// IsDomainClaimedByOtherOrg reports whether err is the backend's refusal to
// create an organization for a caller whose email domain another organization
// already claims (KGI_STA_002).
func IsDomainClaimedByOtherOrg(err error) bool {
	return HasErrorCode(err, ErrCodeDomainClaimedByOtherOrg)
}

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
