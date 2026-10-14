// Package fsutil has small file helpers: atomic writes, copies, moves and
// cached SHA-256 hashes.
package fsutil

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync"
)

func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type hashEntry struct {
	size  int64
	mtime int64
	hash  string
}

var hashCache = struct {
	sync.Mutex
	m map[string]hashEntry
}{m: map[string]hashEntry{}}

// CachedSHA256 hashes a file, reusing the result while size and mtime match.
// Files the loader writes itself are recorded with SetHashCache, so a rewrite
// within the filesystem's timestamp granularity can't serve a stale hash.
func CachedSHA256(path string, info os.FileInfo) (string, error) {
	hashCache.Lock()
	e, ok := hashCache.m[path]
	hashCache.Unlock()
	if ok && e.size == info.Size() && e.mtime == info.ModTime().UnixNano() {
		return e.hash, nil
	}
	h, err := SHA256File(path)
	if err == nil {
		SetHashCache(path, info, h)
	}
	return h, err
}

func SetHashCache(path string, info os.FileInfo, h string) {
	hashCache.Lock()
	defer hashCache.Unlock()
	if len(hashCache.m) > 500 {
		hashCache.m = map[string]hashEntry{}
	}
	if info == nil {
		delete(hashCache.m, path)
		return
	}
	hashCache.m[path] = hashEntry{info.Size(), info.ModTime().UnixNano(), h}
}

func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func CopyFileAtomic(src, dst string) error {
	tmp := dst + ".frogtmp"
	if err := CopyFile(src, tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// MoveFile renames, falling back to copy+delete across drives.
func MoveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := CopyFile(src, dst); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

func WriteFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func Exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
