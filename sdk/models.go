// Package kagi provides a read-only Go SDK for the Kagi secrets management API.
package kagi

// Project represents a Kagi project.
type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
}

// App represents a Kagi app within a project.
type App struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
}

// Environment represents a Kagi environment within a project.
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
	ID           string `json:"id"`
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	Subject      string `json:"subject"`
	SANs         string `json:"sans"`
	Thumbprint   string `json:"thumbprint"`
	NotAfter     string `json:"notAfter"`
	Source       string `json:"source"`
	BindingCount int64  `json:"bindingCount"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
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
	BindingCount     int64  `json:"bindingCount"`
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

// CertificateBinding represents a certificate-to-app-environment binding.
type CertificateBinding struct {
	ID              string `json:"id"`
	CertificateID   string `json:"certificateId"`
	ProjectID       string `json:"projectId"`
	ProjectName     string `json:"projectName"`
	AppID           string `json:"appId"`
	AppName         string `json:"appName"`
	EnvironmentID   string `json:"environmentId"`
	EnvironmentName string `json:"environmentName"`
	CreatedAt       string `json:"createdAt"`
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
