package mods

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
)

// VPKSummary says roughly what a VPK contains, read from its directory tree.
type VPKSummary struct {
	Files   int      `json:"files"`
	Folders []string `json:"folders"` // the folders with the most files (up to 3)
}

const (
	maxVPKTree  = 64 << 20 // a directory tree larger than this isn't a mod
	maxVPKFiles = 1 << 20
)

// SummarizeVPK reads a Valve VPK (v1 or v2) directory tree: the header
// (signature 0x55aa1234, version, tree size; v2 adds four more sizes), then
// extension / path / file name null-terminated strings, each file followed by
// an 18-byte entry and its preload bytes.
func SummarizeVPK(path string) (*VPKSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var hdr [3]uint32
	if err := binary.Read(f, binary.LittleEndian, &hdr); err != nil {
		return nil, errors.New("not a VPK")
	}
	if hdr[0] != vpkMagic {
		return nil, errors.New("not a VPK")
	}
	switch hdr[1] {
	case 1:
	case 2:
		if _, err := io.CopyN(io.Discard, f, 16); err != nil {
			return nil, errors.New("truncated VPK header")
		}
	default:
		return nil, errors.New("unknown VPK version")
	}
	if hdr[2] > maxVPKTree {
		return nil, errors.New("VPK directory too large")
	}
	r := bufio.NewReader(io.LimitReader(f, int64(hdr[2])))
	str := func() (string, error) {
		s, err := r.ReadString(0)
		if err != nil {
			return "", errors.New("truncated VPK directory")
		}
		return s[:len(s)-1], nil
	}
	perFolder := map[string]int{}
	files := 0
	for {
		ext, err := str()
		if err != nil {
			return nil, err
		}
		if ext == "" {
			break
		}
		for {
			dir, err := str()
			if err != nil {
				return nil, err
			}
			if dir == "" {
				break
			}
			for {
				name, err := str()
				if err != nil {
					return nil, err
				}
				if name == "" {
					break
				}
				var entry struct {
					CRC          uint32
					PreloadBytes uint16
					ArchiveIndex uint16
					Offset       uint32
					Length       uint32
					Terminator   uint16
				}
				if err := binary.Read(r, binary.LittleEndian, &entry); err != nil || entry.Terminator != 0xffff {
					return nil, errors.New("broken VPK directory entry")
				}
				if _, err := r.Discard(int(entry.PreloadBytes)); err != nil {
					return nil, errors.New("truncated VPK directory")
				}
				if files++; files > maxVPKFiles {
					return nil, errors.New("too many files in VPK")
				}
				folder := strings.Trim(dir, "/ ")
				if folder == "" {
					folder = "(top level)"
				}
				perFolder[folder]++
			}
		}
	}
	s := &VPKSummary{Files: files, Folders: []string{}}
	for d := range perFolder {
		s.Folders = append(s.Folders, d)
	}
	sort.Slice(s.Folders, func(i, j int) bool {
		a, b := s.Folders[i], s.Folders[j]
		if perFolder[a] != perFolder[b] {
			return perFolder[a] > perFolder[b]
		}
		return a < b
	})
	if len(s.Folders) > 3 {
		s.Folders = s.Folders[:3]
	}
	return s, nil
}
