package gameinfo

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fr0gtech/frogtech-mod-loader/internal/fsutil"
	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
)

// Config mods ("performance configs") ship a whole gameinfo.gi with their
// settings in it. Putting that file in place of the game's would freeze the
// rest of it at the game version the author had, so the loader merges
// instead: every value of the mod's file goes into the game's current file,
// key by key. The FileSystem block (the search paths) stays the loader's.
//
// A merged file starts with the marker line, and Valve's file it was made
// from is kept next to it (BaseSuffix), so switching the config off or
// "Start without mods" gives Valve's file back exactly. A game update
// replaces the file: no marker, so that one becomes the new base.

// Config is one config mod's gameinfo.gi.
type Config struct {
	Name string // shown in the marker line
	Text string
}

const (
	marker = "// Frogtech Mod Loader merged game settings into this file from: "
	// BaseSuffix: gameinfo.gi + this is Valve's file a merged one was made from.
	BaseSuffix = ".frogbase"
	// MaxConfigSize is the biggest gameinfo.gi a mod may ship.
	MaxConfigSize = 2 << 20
)

type token struct {
	start, end int // byte range in the text, quotes included
	text       string
	quoted     bool
	brace      byte // '{' or '}', else 0
}

func lex(s string) []token {
	var out []token
	i, n := 0, len(s)
	if strings.HasPrefix(s, "\xef\xbb\xbf") { // UTF-8 byte order mark
		i = 3
	}
	for i < n {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case c == '/' && i+1 < n && s[i+1] == '/':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				i = n
			} else {
				i += j
			}
		case c == '{' || c == '}':
			out = append(out, token{start: i, end: i + 1, brace: c})
			i++
		case c == '"':
			j := strings.IndexByte(s[i+1:], '"')
			end := n
			if j >= 0 {
				end = i + 1 + j + 1
			}
			text := s[i+1 : end]
			if j >= 0 {
				text = s[i+1 : end-1]
			}
			out = append(out, token{start: i, end: end, text: text, quoted: true})
			i = end
		default:
			j := i
			for j < n && !strings.ContainsRune(" \t\r\n{}\"", rune(s[j])) {
				j++
			}
			out = append(out, token{start: i, end: j, text: s[i:j]})
			i = j
		}
	}
	return out
}

type node struct {
	key         token
	val         token // scalar value
	block       bool
	open, close int // byte offsets of the block's braces
	kids        []*node
}

func parseNodes(toks []token, pos *int, inner bool) ([]*node, int, error) {
	var out []*node
	for *pos < len(toks) {
		k := toks[*pos]
		*pos++
		if k.brace == '}' {
			if !inner {
				return nil, 0, errors.New("a } without its {")
			}
			return out, k.start, nil
		}
		if k.brace == '{' || *pos >= len(toks) {
			return nil, 0, errors.New("a key without a value")
		}
		v := toks[*pos]
		*pos++
		switch v.brace {
		case '}':
			return nil, 0, fmt.Errorf("the key %q has no value", k.text)
		case '{':
			kids, end, err := parseNodes(toks, pos, true)
			if err != nil {
				return nil, 0, err
			}
			out = append(out, &node{key: k, block: true, open: v.start, close: end, kids: kids})
		default:
			out = append(out, &node{key: k, val: v})
		}
	}
	if inner {
		return nil, 0, errors.New("a { is never closed")
	}
	return out, 0, nil
}

// parseGameInfo returns the GameInfo block of a gameinfo.gi.
func parseGameInfo(s string) (*node, error) {
	pos := 0
	nodes, _, err := parseNodes(lex(s), &pos, false)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if n.block && strings.EqualFold(n.key.text, "GameInfo") {
			return n, nil
		}
	}
	return nil, errors.New("no GameInfo block")
}

// CheckConfig reports whether text is a gameinfo.gi the loader can merge.
func CheckConfig(text string) error {
	if len(text) > MaxConfigSize {
		return errors.New("the gameinfo.gi is too big")
	}
	if _, err := parseGameInfo(text); err != nil {
		return fmt.Errorf("the gameinfo.gi can't be read: %w", err)
	}
	return nil
}

type edit struct {
	start, end int
	text       string
}

func show(t token) string {
	if t.quoted || t.text == "" || strings.ContainsAny(t.text, " \t") {
		return `"` + strings.ReplaceAll(t.text, `"`, "") + `"`
	}
	return t.text
}

func writeNode(b *strings.Builder, n *node, indent, eol string) {
	if !n.block {
		b.WriteString(indent + show(n.key) + "\t" + show(n.val) + eol)
		return
	}
	b.WriteString(indent + show(n.key) + eol + indent + "{" + eol)
	for _, k := range n.kids {
		writeNode(b, k, indent+"\t", eol)
	}
	b.WriteString(indent + "}" + eol)
}

// nth returns the i-th child of b with this key and kind.
func nth(b *node, key string, block bool, i int) *node {
	for _, k := range b.kids {
		if k.block == block && strings.EqualFold(k.key.text, key) {
			if i == 0 {
				return k
			}
			i--
		}
	}
	return nil
}

func hasKey(b *node, key string) bool {
	for _, k := range b.kids {
		if strings.EqualFold(k.key.text, key) {
			return true
		}
	}
	return false
}

func mergeBlock(base string, b, c *node, top bool, eol string, edits *[]edit) {
	seen := map[string]int{}
	var add []*node
	for _, ck := range c.kids {
		if top && ck.block && strings.EqualFold(ck.key.text, "FileSystem") {
			continue // the search paths are the loader's
		}
		id := strings.ToLower(ck.key.text)
		if ck.block {
			id += "{"
		}
		i := seen[id]
		seen[id]++
		bk := nth(b, ck.key.text, ck.block, i)
		switch {
		case bk == nil && i == 0 && hasKey(b, ck.key.text):
			// a value where the game has a block (or the other way): leave it
		case bk == nil:
			add = append(add, ck)
		case ck.block:
			mergeBlock(base, bk, ck, false, eol, edits)
		case bk.val.text != ck.val.text:
			*edits = append(*edits, edit{bk.val.start, bk.val.end, show(token{text: ck.val.text, quoted: bk.val.quoted || ck.val.quoted})})
		}
	}
	if len(add) == 0 {
		return
	}
	// new lines go right before the block's }, one tab deeper than it
	ls := strings.LastIndexByte(base[:b.close], '\n') + 1
	indent, at, lead := base[ls:b.close], ls, ""
	if strings.TrimSpace(indent) != "" {
		indent, at, lead = "", b.close, eol
	}
	var sb strings.Builder
	sb.WriteString(lead)
	for _, n := range add {
		writeNode(&sb, n, indent+"\t", eol)
	}
	if lead != "" {
		sb.WriteString(indent)
	}
	*edits = append(*edits, edit{at, at, sb.String()})
}

// Overlay returns the gameinfo.gi text base with every value of cfg (another
// gameinfo.gi) set in it: existing keys get cfg's value, new keys and blocks
// are added, everything else stays. The FileSystem block is not touched.
func Overlay(base, cfg string) (string, error) {
	b, err := parseGameInfo(base)
	if err != nil {
		return "", fmt.Errorf("the game's gameinfo.gi: %w", err)
	}
	c, err := parseGameInfo(cfg)
	if err != nil {
		return "", err
	}
	eol := "\n"
	if strings.Contains(base, "\r\n") {
		eol = "\r\n"
	}
	var edits []edit
	mergeBlock(base, b, c, true, eol, &edits)
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := base
	for _, e := range edits {
		out = out[:e.start] + e.text + out[e.end:]
	}
	return out, nil
}

// Merged reports whether content is a file the loader merged configs into.
func Merged(content string) bool { return strings.HasPrefix(content, marker) }

func dropMarker(content string) string {
	if !Merged(content) {
		return content
	}
	if i := strings.IndexByte(content, '\n'); i >= 0 {
		return content[i+1:]
	}
	return ""
}

// build returns what gameinfo.gi should be: Valve's file (base), the configs
// merged in (the first one wins where two set the same key), and the loader's
// search paths when addons is set. A config that can't be merged is left out
// and named in skipped.
func build(base string, addons bool, cfgs []Config) (out string, skipped []string, err error) {
	out = base
	var names []string
	for i := len(cfgs) - 1; i >= 0; i-- {
		next, err := Overlay(out, cfgs[i].Text)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", cfgs[i].Name, err))
			continue
		}
		out = next
		if cfgs[i].Name != "" { // (a nameless one is a player's changes to the next)
			names = append([]string{strings.NewReplacer("\r", " ", "\n", " ").Replace(cfgs[i].Name)}, names...)
		}
	}
	if out != base && len(names) == 0 {
		names = []string{"your settings"}
	}
	if len(names) > 0 {
		eol := "\n"
		if strings.Contains(base, "\r\n") {
			eol = "\r\n"
		}
		out = marker + strings.Join(names, ", ") + eol + out
	}
	if addons {
		if out, _, err = Ensure(out); err != nil {
			return "", skipped, err
		}
	}
	return out, skipped, nil
}

// set writes gameinfo.gi as build makes it, from Valve's file: the current
// one without the loader's lines, or the kept base of a merged one.
func set(gameDir string, addons bool, cfgs []Config) (bool, error) {
	_, _, path := steam.Paths(gameDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	cur := string(data)
	merged := Merged(cur)
	base := cur
	if merged {
		if b, err := os.ReadFile(path + BaseSuffix); err == nil {
			base = string(b)
		} else {
			base = dropMarker(cur) // the kept file is gone: go on from this one
		}
	}
	if base, _, err = Strip(base); err != nil {
		return false, err
	}
	want, skipped, err := build(base, addons, cfgs)
	if err != nil {
		return false, err
	}
	var skipErr error
	if len(skipped) > 0 {
		skipErr = fmt.Errorf("game settings not merged (%s)", strings.Join(skipped, "; "))
	}
	if want == cur {
		return false, skipErr
	}
	if Merged(want) && !merged {
		if err := fsutil.WriteFileAtomic(path+BaseSuffix, []byte(base)); err != nil {
			return false, err
		}
	}
	if err := fsutil.WriteFileAtomic(path, []byte(want)); err != nil {
		return false, err
	}
	if !Merged(want) {
		os.Remove(path + BaseSuffix)
	}
	return true, skipErr
}
