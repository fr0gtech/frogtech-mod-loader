package main

import "encoding/base64"

// secretStore protects repository keys at rest. On Windows it is DPAPI
// (CryptProtectData for the current user, secrets_windows.go); elsewhere a
// stand-in for development and tests (secrets_other.go).
type secretStore interface {
	Protect(plain []byte) ([]byte, error)
	Unprotect(blob []byte) ([]byte, error)
}

var secrets secretStore = newSecretStore()

func sealKey(key string) (string, error) {
	blob, err := secrets.Protect([]byte(key))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(blob), nil
}

func openKey(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	blob, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	plain, err := secrets.Unprotect(blob)
	return string(plain), err
}
