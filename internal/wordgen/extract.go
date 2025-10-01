package wordgen

import (
	"log"
	"net/url"
	"path"
	"sort"
	"strings"
)

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool { return r >= 'a' && r <= 'z' }
func isDigit(r rune) bool { return r >= '0' && r <= '9' }
func isAlphaNum(r rune) bool { return isLower(r) || isUpper(r) || isDigit(r) }

func splitCamelToken(tok string) []string {
	r := []rune(tok)
	if len(r) == 0 { return nil }
	var parts []string
	start := 0
	for i := 1; i < len(r); i++ {
		prev, cur := r[i-1], r[i]
		boundary := false
		if isLower(prev) && isUpper(cur) { boundary = true }
		if isUpper(prev) && isUpper(cur) && i+1 < len(r) && isLower(r[i+1]) { boundary = true }
		if (isDigit(prev) && !isDigit(cur)) || (!isDigit(prev) && isDigit(cur)) { boundary = true }
		if boundary { parts = append(parts, string(r[start:i])); start = i }
	}
	parts = append(parts, string(r[start:]))
	return parts
}

func sanitizeToTokens(s string) []string {
	if s == "" { return nil }
	var b strings.Builder
	for _, r := range s { if isAlphaNum(r) { b.WriteRune(r) } else { b.WriteByte(' ') } }
	raw := strings.Fields(b.String())
	out := make([]string, 0, len(raw))
	for _, tok := range raw {
		for _, p := range splitCamelToken(tok) {
			p = strings.ToLower(strings.TrimSpace(p))
			if p != "" { out = append(out, p) }
		}
	}
	return out
}

// FromURLs extracts unique tokens from URL paths and query keys
func FromURLs(urls []string, debug bool) []string {
	if debug {
		log.Printf("DEBUG: Generating wordlist from %d URLs", len(urls))
	}
	set := map[string]struct{}{}
	add := func(w string) { w = strings.ToLower(strings.TrimSpace(w)); if w != "" { set[w] = struct{}{} } }

	for _, s := range urls {
		u, err := url.Parse(s)
		if err != nil { continue }
		segs := strings.Split(u.Path, "/")
		for _, seg := range segs {
			seg = strings.TrimSpace(seg)
			if seg == "" { continue }
			add(seg)
			for _, t := range sanitizeToTokens(seg) { add(t) }
			if debug && seg != "" {
				log.Printf("DEBUG: Added path segment: %s", seg)
			}
		}
		for k := range u.Query() {
			add(k)
			for _, t := range sanitizeToTokens(k) { add(t) }
			if debug && k != "" {
				log.Printf("DEBUG: Added query parameter: %s", k)
			}
		}
		if base := path.Base(u.Path); base != "" && base != "/" {
			name := strings.TrimSuffix(base, path.Ext(base))
			if name != "" && name != base {
				add(name)
				for _, t := range sanitizeToTokens(name) { add(t) }
				if debug && name != "" && name != base {
					log.Printf("DEBUG: Added base name: %s (from %s)", name, base)
				}
			}
		}
	}
	list := make([]string, 0, len(set))
	for w := range set { list = append(list, w) }
	sort.Strings(list)
	if debug {
		log.Printf("DEBUG: Generated %d unique words", len(list))
	}
	return list
}
