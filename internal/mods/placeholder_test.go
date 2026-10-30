package mods

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// vpkPyEmptySHA256 is the sha256 of `tools/vpk.py pack <empty folder> out`
// from the UI Kit repository (the writer whose VPKs the game loads).
const vpkPyEmptySHA256 = "d4398d7ab1fdde113a492f1ff61d91b1f6378c793fdd7fbedc32e6bae85a7ccf"

func TestPlaceholderIsValidVPK(t *testing.T) {
	b := PlaceholderVPK()
	if len(b) < 28+1+48 {
		t.Fatalf("too short: %d bytes", len(b))
	}
	var hdr [7]uint32
	for i := range hdr {
		hdr[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
	if hdr[0] != 0x55aa1234 || hdr[1] != 2 {
		t.Fatalf("header %x", hdr)
	}
	if hdr[2] != 1 || b[28] != 0 {
		t.Fatalf("tree size %d / terminator %x, want 1 / 00", hdr[2], b[28])
	}
	// v2 sections: no file data, no archive MD5s, 48 bytes of MD5s, no signature
	if hdr[3] != 0 || hdr[4] != 0 || hdr[5] != 48 || hdr[6] != 0 {
		t.Fatalf("v2 section sizes %v", hdr[3:])
	}
	if len(b) != 28+1+48 {
		t.Fatalf("length %d, want %d", len(b), 28+1+48)
	}
	treeMD5 := md5.Sum(b[28:29])
	archMD5 := md5.Sum(nil)
	if string(b[29:45]) != string(treeMD5[:]) || string(b[45:61]) != string(archMD5[:]) {
		t.Fatal("tree / archive MD5 wrong")
	}
	whole := md5.Sum(b[:61])
	if string(b[61:77]) != string(whole[:]) {
		t.Fatal("whole-file MD5 wrong")
	}
	if !isVPKHeader(b) || archiveKind(b) != "vpk" {
		t.Fatal("not sniffed as a VPK")
	}
	// The loader's own VPK reader reads it as an empty tree.
	p := filepath.Join(t.TempDir(), "pak01_dir.vpk")
	os.WriteFile(p, b, 0o644)
	sum, err := SummarizeVPK(p)
	if err != nil || sum.Files != 0 {
		t.Fatalf("SummarizeVPK: %+v %v", sum, err)
	}
	// Byte-identical to what the UI Kit's vpk.py writes for an empty folder.
	h := sha256.Sum256(b)
	if got := hex.EncodeToString(h[:]); got != vpkPyEmptySHA256 {
		t.Fatalf("sha256 %s, vpk.py writes %s", got, vpkPyEmptySHA256)
	}
}
