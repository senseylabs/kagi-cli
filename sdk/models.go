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
	Name string `json:"name"`
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

// APIResponse wraps the standard Kagi API response envelope.
type APIResponse[T any] struct {
	Data    T      `json:"data"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}
