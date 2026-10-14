package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractLoaderExe(t *testing.T) {
	dir := t.TempDir()
	exe := []byte("MZ\x90\x00fake windows program")
	pkg := filepath.Join(dir, "FrogLoader_v9.zip")
	writeZip(t, pkg, map[string][]byte{"FrogLoader.exe": exe, "readme.txt": []byte("x")})
	out := filepath.Join(dir, "FrogLoader.exe.new")
	if err := extractLoaderExe(pkg, out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != string(exe) {
		t.Fatal("wrong content")
	}
	// A bare exe (older catalog format) is copied as is.
	bare := filepath.Join(dir, "bare.bin")
	os.WriteFile(bare, exe, 0o644)
	if err := extractLoaderExe(bare, out); err != nil {
		t.Fatal(err)
	}
	// Zip without the exe, or with something that isn't a PE file.
	writeZip(t, pkg, map[string][]byte{"other.exe": exe})
	if err := extractLoaderExe(pkg, out); err == nil {
		t.Fatal("zip without FrogLoader.exe accepted")
	}
	writeZip(t, pkg, map[string][]byte{"sub/FrogLoader.exe": []byte("#!/bin/sh")})
	if err := extractLoaderExe(pkg, out); err == nil {
		t.Fatal("non-PE FrogLoader.exe accepted")
	}
}
