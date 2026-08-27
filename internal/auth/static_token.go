package auth

import (
	"os"
	"strings"
)

// StaticTokenEnv is the environment variable carrying a long-lived Kagi access
// token (a Personal Access Token, minted in the web console) for
// non-interactive use — CI runners and other machines where no human can
// complete the Device Authorization Grant.
const StaticTokenEnv = "KAGI_TOKEN"

// StaticToken returns the access token supplied via KAGI_TOKEN, or the empty
// string when the variable is unset, empty, or whitespace only. An empty value
// is deliberately indistinguishable from unset: `KAGI_TOKEN=` in a CI job (an
// unset secret expanding to nothing) must fall through to the stored session
// rather than authenticate with an empty bearer token and fail as a 401.
//
// Surrounding whitespace is trimmed. A token sourced from a file or a secret
// store commonly carries a trailing newline, and a bearer credential never
// legitimately contains leading or trailing whitespace.
func StaticToken() string {
	return strings.TrimSpace(os.Getenv(StaticTokenEnv))
}
