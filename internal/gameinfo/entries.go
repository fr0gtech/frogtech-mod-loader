package gameinfo

import (
	"regexp"
	"strings"
)

// Entry is one console variable of a config mod's ConVars block, with what
// the mod's author wrote next to it. Config authors document their files in
// the comments ("value" // default: "x" | what it does), and that text is
// the only description there is: the game ships none for most variables.
type Entry struct {
	Key     string `json:"key"`
	Value   string `json:"value"`             // the mod's value
	Default string `json:"default,omitempty"` // the game's own value, as the author gives it
	Note    string `json:"note,omitempty"`    // the author's description
	Section string `json:"section,omitempty"` // the heading it stands under
	// Off: the author left the line commented out (an option to switch on).
	Off bool `json:"off,omitempty"`
}

var (
	entryRe   = regexp.MustCompile(`^\s*"?([A-Za-z_][\w.]*)"?\s+(?:"([^"]*)"|([^\s"/{}]+))\s*(?://(.*))?$`)
	offRe     = regexp.MustCompile(`^\s*//\s*"?([a-z][a-z0-9]*_[\w.]*)"?\s+"([^"]*)"\s*(?://(.*))?$`)
	sectionRe = regexp.MustCompile(`^\s*//\s*[-=]{2,}\s*(.*?[^-=\s])\s*[-=]{2,}[\s\\]*$`)
	defaultRe = regexp.MustCompile(`(?i)^\s*default:?\s*"?([^"|\s]*)"?\s*(?:\||$)`)
)

// ValidKey reports whether s can be a console variable's name.
func ValidKey(s string) bool {
	return len(s) <= 96 && regexp.MustCompile(`^[A-Za-z_][\w.]*$`).MatchString(s)
}

// ValidValue reports whether s can be written as a value into gameinfo.gi.
func ValidValue(s string) bool {
	return len(s) <= 64 && !strings.ContainsAny(s, "\"{}\r\n\\") && strings.TrimSpace(s) == s
}

func comment(c string) (def, note string) {
	c = strings.TrimSpace(c)
	if m := defaultRe.FindStringSubmatch(c); m != nil {
		def = m[1]
		c = c[len(m[0]):]
	}
	c = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(c), "|"))
	c = strings.TrimSpace(strings.TrimRight(c, `\ `))
	if len(c) > 600 {
		c = c[:600] + "…"
	}
	return def, c
}

// Entries lists the variables in the ConVars block of a config mod's
// gameinfo.gi, in the file's order: the set ones and the commented-out ones.
func Entries(text string) []Entry {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	start := -1
	for i, l := range lines {
		if strings.EqualFold(strings.Trim(stripComment(l), `"`), "ConVars") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil
	}
	var out []Entry
	at := map[string]int{}
	depth, section := 0, ""
	for _, l := range lines[start:] {
		switch code := stripComment(l); {
		case code == "{":
			depth++
			continue
		case code == "}":
			depth--
			if depth <= 0 {
				return out
			}
			continue
		}
		if depth != 1 {
			continue
		}
		if m := sectionRe.FindStringSubmatch(l); m != nil {
			if len(m[1]) <= 60 && !strings.Contains(m[1], "://") {
				section = strings.TrimSpace(strings.TrimLeft(m[1], "0123456789. "))
				if section == "" {
					section = m[1]
				}
				if strings.HasPrefix(strings.ToUpper(section), "END OF") {
					section = "Other"
				}
			}
			continue
		}
		var e Entry
		if m := offRe.FindStringSubmatch(l); m != nil {
			e = Entry{Key: m[1], Value: m[2], Off: true}
			e.Default, e.Note = comment(m[3])
		} else if m := entryRe.FindStringSubmatch(l); m != nil {
			e = Entry{Key: m[1], Value: m[2] + m[3]}
			e.Default, e.Note = comment(m[4])
		} else {
			continue
		}
		if e.Off && section == "" {
			continue // the file's intro (example lines), not an option
		}
		e.Section = section
		if i, ok := at[strings.ToLower(e.Key)]; ok {
			if e.Off {
				continue // the set line counts
			}
			if !out[i].Off {
				continue // set twice: the first one is shown
			}
			out[i] = e
			continue
		}
		at[strings.ToLower(e.Key)] = len(out)
		out = append(out, e)
	}
	return out
}

// ConVarsConfig is a gameinfo.gi with only these console variables, to merge
// over a config mod's own (the player's changes to it).
func ConVarsConfig(vals map[string]string, order []string) string {
	var b strings.Builder
	b.WriteString("GameInfo\n{\n\tConVars\n\t{\n")
	for _, k := range order {
		if v, ok := vals[k]; ok && ValidKey(k) && ValidValue(v) {
			b.WriteString("\t\t\"" + k + "\"\t\"" + v + "\"\n")
		}
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}
