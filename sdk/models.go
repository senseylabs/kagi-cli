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

// APIResponse wraps the standard Kagi API response envelope.
type APIResponse[T any] struct {
	Data    T      `json:"data"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}
