// Package secrets protects repository keys at rest: Windows DPAPI for the
// current user, a stand-in elsewhere (development and tests).
package secrets

import "encoding/base64"

// secretStore protects repository keys at rest. On Windows it is DPAPI
// (CryptProtectData for the current user, dpapi_windows.go); elsewhere a
// stand-in for development and tests (fake_other.go).
type secretStore interface {
	Protect(plain []byte) ([]byte, error)
	Unprotect(blob []byte) ([]byte, error)
}

var store secretStore = newSecretStore()

// Seal protects a key and returns it base64-encoded for state.json.
func Seal(key string) (string, error) {
	blob, err := store.Protect([]byte(key))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(blob), nil
}

// Open reverses Seal ("" stays "").
func Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	blob, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	plain, err := store.Unprotect(blob)
	return string(plain), err
}
