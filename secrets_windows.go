//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

type dpapiStore struct{}

func newSecretStore() secretStore { return dpapiStore{} }

var dpapiEntropy = []byte("FrogLoader repository key")

func blobOf(b []byte) *windows.DataBlob {
	if len(b) == 0 {
		return &windows.DataBlob{}
	}
	return &windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

func (dpapiStore) Protect(plain []byte) ([]byte, error) {
	var out windows.DataBlob
	desc, _ := windows.UTF16PtrFromString("Frogtech Mod Loader repository key")
	if err := windows.CryptProtectData(blobOf(plain), desc, blobOf(dpapiEntropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func (dpapiStore) Unprotect(blob []byte) ([]byte, error) {
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(blobOf(blob), nil, blobOf(dpapiEntropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
