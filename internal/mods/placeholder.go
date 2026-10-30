package mods

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// PlaceholderVPK is the smallest valid VPK v2: an empty directory tree. It
// takes the pak numbers of a parked mod (one whose files were built for
// another game build), so the mods after it keep their numbers.
//
// The bytes are what the UI Kit's tools/vpk.py writes for an empty folder:
// header (signature 0x55aa1234, version 2, tree size 1, file data 0, archive
// MD5s 0, other MD5s 48, signature 0), the tree (one empty extension string:
// "\x00"), then the MD5 of the tree, of the (empty) archive MD5 section and of
// everything before.
func PlaceholderVPK() []byte {
	tree := []byte{0}
	b := make([]byte, 28, 28+len(tree)+48)
	for i, v := range []uint32{vpkMagic, 2, uint32(len(tree)), 0, 0, 48, 0} {
		binary.LittleEndian.PutUint32(b[i*4:], v)
	}
	b = append(b, tree...)
	treeMD5, archMD5 := md5.Sum(tree), md5.Sum(nil)
	b = append(b, treeMD5[:]...)
	b = append(b, archMD5[:]...)
	whole := md5.Sum(b)
	return append(b, whole[:]...)
}

func placeholderSHA256() string {
	h := sha256.Sum256(PlaceholderVPK())
	return hex.EncodeToString(h[:])
}
