// Package kagi provides a read-only Go SDK for the Kagi secrets management API.
//
// Addressing follows the folder model: secrets are addressed by an app's stable
// internal ID plus an environment slug (the durable machine binding), while
// folder paths are used only for browsing and one-time path -> app-ID
// resolution at setup. Folder IDs are never sent by the SDK.
package kagi

// KagiLibrary is the URL slug identifying a Kagi folder library.
type KagiLibrary string

// Library slugs as accepted by the folder-browse routes.
const (
	LibrarySecrets       KagiLibrary = "secrets"
	LibraryPasswords     KagiLibrary = "passwords"
	LibraryAuthenticator KagiLibrary = "authenticator"
	LibraryCertificates  KagiLibrary = "certificates"
	LibraryAccessTokens  KagiLibrary = "access-tokens"
)

// Organization represents a Kagi organization the user belongs to.
type Organization struct {
	ID   string `json:"id"`
	Name string `json:"displayName"`
	Slug string `json:"slug"`
}

// App represents a Kagi app exposed by a SECRETS folder's children listing.
// The ID is the stable machine binding used to address secrets; renaming or
// moving an app never changes it.
type App struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// Folder represents a folder node within a Kagi library.
type Folder struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Slug       string  `json:"slug"`
	Path       string  `json:"path"`
	Library    string  `json:"library"`
	ParentID   *string `json:"parentId"`
	OwnerID    *string `json:"ownerId"`
	SystemRoot bool    `json:"systemRoot"`
	CreatedAt  string  `json:"createdAt"`
	UpdatedAt  string  `json:"updatedAt"`
}

// FolderChildren is the result of browsing a folder: its child folders and,
// for the SECRETS library, the apps directly under it. Apps is empty for
// non-SECRETS libraries.
type FolderChildren struct {
	Path    string   `json:"path"`
	Folders []Folder `json:"folders"`
	Apps    []App    `json:"apps"`
}

// Environment represents a Kagi environment within an app.
type Environment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// SecretFetchResponse holds decrypted secrets as key-value pairs.
type SecretFetchResponse struct {
	Secrets map[string]string `json:"secrets"`
}

// CertificateListItem represents a certificate in list view.
type CertificateListItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	Subject    string `json:"subject"`
	SANs       string `json:"sans"`
	Thumbprint string `json:"thumbprint"`
	NotAfter   string `json:"notAfter"`
	Source     string `json:"source"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

// CertificateDetail represents full certificate metadata.
type CertificateDetail struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Slug             string `json:"slug"`
	Subject          string `json:"subject"`
	Issuer           string `json:"issuer"`
	Thumbprint       string `json:"thumbprint"`
	SerialNumber     string `json:"serialNumber"`
	SANs             string `json:"sans"`
	NotBefore        string `json:"notBefore"`
	NotAfter         string `json:"notAfter"`
	ContentType      string `json:"contentType"`
	Source           string `json:"source"`
	SourceIdentifier string `json:"sourceIdentifier"`
	SourceVaultName  string `json:"sourceVaultName"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
}

// CertificateFolderItem represents a certificate leaf listed directly inside a
// certificate folder. It mirrors the SECRETS App leaf: the ID is the stable
// machine binding used to address the certificate's TLS content, unchanged
// across renames and folder moves. Served by the /items folder endpoint (the
// certificates library's children listing carries folders only).
type CertificateFolderItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	Subject    string `json:"subject"`
	SANs       string `json:"sans"`
	Thumbprint string `json:"thumbprint"`
	NotAfter   string `json:"notAfter"`
	Source     string `json:"source"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

// CertificateResolve is the response of resolving a certificate node path to its
// stable id, the certificate analog of the secrets app-resolve step.
type CertificateResolve struct {
	CertificateID string `json:"certificateId"`
	Name          string `json:"name"`
}

// CertificateReveal holds decrypted certificate and private key content.
type CertificateReveal struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	CertificateContent string `json:"certificateContent"`
	PrivateKeyContent  string `json:"privateKeyContent"`
}

// CertificateHistory represents an audit history entry for a certificate.
type CertificateHistory struct {
	ID            string `json:"id"`
	CertificateID string `json:"certificateId"`
	ChangeType    string `json:"changeType"`
	Thumbprint    string `json:"thumbprint"`
	NotAfter      string `json:"notAfter"`
	ChangedBy     string `json:"changedBy"`
	CreatedAt     string `json:"createdAt"`
}

// PasswordListItem represents a password leaf held directly inside a PASSWORDS
// folder, with a masked value. Passwords carry no name or slug — a credential is
// identified by its login username and service URL. The ID is the stable machine
// binding used to address the password's by-id read/reveal/history endpoints,
// unchanged across folder moves. The by-id detail endpoint returns this same
// shape, so it doubles as the detail model. HasLinkedTOTP reports whether a 2FA
// authenticator item is linked; the Linked* fields are populated only when the
// caller can reach that linked code (empty otherwise).
type PasswordListItem struct {
	ID                        string `json:"id"`
	Username                  string `json:"username"`
	URL                       string `json:"url"`
	MaskedPassword            string `json:"maskedPassword"`
	HasNotes                  bool   `json:"hasNotes"`
	HasLinkedTOTP             bool   `json:"hasLinkedTotp"`
	LinkedAuthenticatorItemID string `json:"linkedAuthenticatorItemId"`
	LinkedTOTPFolderPath      string `json:"linkedTotpFolderPath"`
	LinkedTOTPLabel           string `json:"linkedTotpLabel"`
	CreatedAt                 string `json:"createdAt"`
	UpdatedAt                 string `json:"updatedAt"`
}

// PasswordResolve is the result of resolving a human-entered password node path
// to its stable id. Passwords have no dedicated resolve endpoint (and no
// name/slug), so resolution browses the parent folder's password leaves and
// matches the final path segment against the login username — the password
// analog of the secrets ResolveApp step. Username echoes the matched credential.
type PasswordResolve struct {
	PasswordID string
	Username   string
}

// PasswordReveal holds a decrypted password value with its username, URL, and
// notes. Username and Notes may be empty for credentials that carry neither
// (e.g. passkeys, secure notes). The Linked* fields carry the linked
// authenticator item id and its owning app id when a 2FA code is linked.
type PasswordReveal struct {
	ID                           string `json:"id"`
	Username                     string `json:"username"`
	URL                          string `json:"url"`
	Password                     string `json:"password"`
	Notes                        string `json:"notes"`
	LinkedAuthenticatorItemID    string `json:"linkedAuthenticatorItemId"`
	LinkedAuthenticatorItemAppID string `json:"linkedAuthenticatorItemAppId"`
}

// PasswordHistory represents an audit history entry for a password. The metadata
// never carries the plaintext value. ChangeType is one of CREATED, UPDATED,
// DELETED, MOVED; PreviousAppID is populated only for a MOVED entry.
type PasswordHistory struct {
	ID            string `json:"id"`
	PasswordID    string `json:"passwordId"`
	Username      string `json:"username"`
	URL           string `json:"url"`
	ChangeType    string `json:"changeType"`
	ChangedBy     string `json:"changedBy"`
	PreviousAppID string `json:"previousAppId"`
	CreatedAt     string `json:"createdAt"`
}

// APIResponse wraps the standard Kagi API response envelope.
type APIResponse[T any] struct {
	Data    T      `json:"data"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

// OnboardingState is the caller's own onboarding situation as reported by
// GET /kagi/onboarding/state. It is the one endpoint an account that has not
// finished onboarding may read about itself: every other route answers
// KGI_SEC_038 until onboarding completes.
type OnboardingState string

// The four states the backend reports. The three that overlap with a bootstrap
// outcome deliberately reuse that outcome's wire string, so the CLI and the
// other Kagi clients share one vocabulary.
const (
	// OnboardingStateRequired means the person has not been placed anywhere
	// yet: they finish setup by creating their own organization or by
	// requesting to join one. It is also the safe reading of an unrecognized
	// state (see OnboardingStatus.EffectiveState).
	OnboardingStateRequired OnboardingState = "ONBOARDING_REQUIRED"
	// OnboardingStateJoinRequestPending means a request to join the
	// organization that owns the caller's email domain has been recorded and is
	// waiting for one of its administrators to approve it.
	OnboardingStateJoinRequestPending OnboardingState = "JOIN_REQUEST_PENDING"
	// OnboardingStateOrgNotAvailable means the organization claiming the
	// caller's email domain cannot take members right now (suspended, billing
	// locked, or on a plan without domain join). This is the one blocked state
	// where creating an own organization stays available.
	OnboardingStateOrgNotAvailable OnboardingState = "ORG_NOT_AVAILABLE"
	// OnboardingStateComplete means the account is ACTIVE and onboarding is
	// done; OrganizationSlug names the workspace to work in.
	OnboardingStateComplete OnboardingState = "COMPLETE"
)

// OnboardingStatus is the payload of GET /kagi/onboarding/state: a caller's own
// onboarding situation.
//
// Nullability follows the state. OrganizationID/OrganizationSlug are populated
// only for COMPLETE; JoinRequestOrganizationName/Slug only for
// JOIN_REQUEST_PENDING and ORG_NOT_AVAILABLE. A JSON null unmarshals to the
// empty string, so callers must treat "" as "not reported" and keep a fallback
// label rather than printing a blank organization name.
//
// CanCreateOwnOrganization is the SERVER's verdict on whether creating an own
// organization is still on offer. It is NOT derivable from State — the backend
// answers false for an ONBOARDING_REQUIRED caller whose email domain is claimed
// by an organization that would refuse the create (KGI_STA_002) — so it must be
// read from the wire, never inferred.
type OnboardingStatus struct {
	State                       OnboardingState `json:"state"`
	UserStatus                  string          `json:"userStatus"`
	OrganizationID              string          `json:"organizationId"`
	OrganizationSlug            string          `json:"organizationSlug"`
	JoinRequestOrganizationName string          `json:"joinRequestOrganizationName"`
	JoinRequestOrganizationSlug string          `json:"joinRequestOrganizationSlug"`
	CanCreateOwnOrganization    bool            `json:"canCreateOwnOrganization"`
}

// EffectiveState returns the state to act on, collapsing anything this build
// does not recognize (a state added by a newer backend, or a missing field) to
// OnboardingStateRequired. That is the safe reading: it points the person at
// setup instead of reporting an approval that may not exist.
func (s OnboardingStatus) EffectiveState() OnboardingState {
	switch s.State {
	case OnboardingStateRequired,
		OnboardingStateJoinRequestPending,
		OnboardingStateOrgNotAvailable,
		OnboardingStateComplete:
		return s.State
	default:
		return OnboardingStateRequired
	}
}

// JoinTargetLabel names the organization a blocked state refers to, preferring
// its display name and falling back to its slug. It returns "" when the backend
// reported neither, so the caller can substitute its own wording rather than
// printing an empty name.
func (s OnboardingStatus) JoinTargetLabel() string {
	if s.JoinRequestOrganizationName != "" {
		return s.JoinRequestOrganizationName
	}
	return s.JoinRequestOrganizationSlug
}
