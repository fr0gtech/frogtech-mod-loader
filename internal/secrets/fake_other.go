//go:build !windows

package secrets

import (
	"bytes"
	"errors"
)

// fakeStore stands in for DPAPI off Windows (development and tests only):
// it obfuscates rather than encrypts, so the plain key never appears in
// state.json, and it refuses blobs it didn't make.
type fakeStore struct{}

func newSecretStore() secretStore { return fakeStore{} }

var fakeMagic = []byte("fake-dpapi:")

func (fakeStore) Protect(plain []byte) ([]byte, error) {
	out := append([]byte(nil), fakeMagic...)
	for i, b := range plain {
		out = append(out, b^byte(0x5a+i))
	}
	return out, nil
}

func (fakeStore) Unprotect(blob []byte) ([]byte, error) {
	if !bytes.HasPrefix(blob, fakeMagic) {
		return nil, errors.New("not a protected key")
	}
	blob = blob[len(fakeMagic):]
	out := make([]byte, len(blob))
	for i, b := range blob {
		out[i] = b ^ byte(0x5a+i)
	}
	return out, nil
}
