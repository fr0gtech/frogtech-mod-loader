// Command pack writes the release zip with fixed timestamps and Go's own
// deflate, so the bytes depend only on the inputs and the Go version
// (reproducible builds; see build.sh).
//
//	go run ./tools/pack -o dist/FrogLoader_v0.5.0.zip dist/FrogLoader.exe dist/THIRD_PARTY_NOTICES.txt
package main

import (
	"archive/zip"
	"compress/flate"
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

func main() {
	out := flag.String("o", "", "output zip")
	flag.Parse()
	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	zw := zip.NewWriter(f)
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestCompression)
	})
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, p := range flag.Args() {
		data, err := os.ReadFile(p)
		if err != nil {
			log.Fatal(err)
		}
		h := &zip.FileHeader{Name: filepath.Base(p), Method: zip.Deflate, Modified: stamp}
		h.SetMode(0o644)
		w, err := zw.CreateHeader(h)
		if err != nil {
			log.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			log.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
}
