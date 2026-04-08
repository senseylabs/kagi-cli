//go:build darwin

package auth

import (
	"encoding/json"
	"fmt"

	"github.com/zalando/go-keyring"
)

const (
	keychainService = "kagi-cli"
	keychainUser    = "default"
)

type keychainStore struct{}

// NewTokenStore returns a macOS Keychain-backed token store.
func NewTokenStore() TokenStore {
	return &keychainStore{}
}

func (k *keychainStore) Save(creds Credentials) error {
	data, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("failed to serialize credentials: %w", err)
	}
	if err := keyring.Set(keychainService, keychainUser, string(data)); err != nil {
		return fmt.Errorf("failed to store credentials in keychain: %w", err)
	}
	return nil
}

func (k *keychainStore) Load() (Credentials, error) {
	data, err := keyring.Get(keychainService, keychainUser)
	if err != nil {
		return Credentials{}, fmt.Errorf("no credentials found in keychain: %w", err)
	}

	var creds Credentials
	if err := json.Unmarshal([]byte(data), &creds); err != nil {
		return Credentials{}, fmt.Errorf("failed to parse credentials from keychain: %w", err)
	}
	return creds, nil
}

func (k *keychainStore) Delete() error {
	if err := keyring.Delete(keychainService, keychainUser); err != nil {
		return fmt.Errorf("failed to delete credentials from keychain: %w", err)
	}
	return nil
}
