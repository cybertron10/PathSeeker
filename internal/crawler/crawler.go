package crawler

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Crawl discovers same-domain URLs up to maxDepth and maxPages
func Crawl(startURL string, maxDepth int, maxPages int) ([]string, error) {
	if maxDepth <= 0 { maxDepth = 1 }
	if maxPages <= 0 { maxPages = 1000 }

	start, err := url.Parse(startURL)
	if err != nil { return nil, fmt.Errorf("invalid start url: %w", err) }
	baseHost := start.Host

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSHandshakeTimeout:   5 * time.Second,
			MaxIdleConns:          400,
			MaxIdleConnsPerHost:   100,
			IdleConnTimeout:       15 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
		},
	}

	type item struct{ u string; d int }
	jobs := make(chan item, 20000)
	visited := make(map[string]bool)
	results := make(map[string]bool)
	var mu sync.Mutex
	pending := &sync.WaitGroup{}
	wg := &sync.WaitGroup{}

	// Case-insensitive patterns with quoted and unquoted attributes, plus raw URLs in text/JS
	urlPatterns := []string{
		`(?i)href\s*=\s*["']([^"']+)["']`,
		`(?i)href\s*=\s*([^\s"'>]+)`,
		`(?i)src\s*=\s*["']([^"']+)["']`,
		`(?i)src\s*=\s*([^\s"'>]+)`,
		`(?i)action\s*=\s*["']([^"']+)["']`,
		`(?i)action\s*=\s*([^\s"'>]+)`,
		`(?i)(?:fetch|XMLHttpRequest|ajax)\s*\(\s*["']([^"']+)["']`,
		`(?i)<a[^>]+href\s*=\s*["']([^"']+)["'][^>]*>`,
		`(?i)<link[^>]+href\s*=\s*["']([^"']+)["'][^>]*>`,
		`(?i)<script[^>]+src\s*=\s*["']([^"']+)["'][^>]*>`,
		`(?i)<img[^>]+src\s*=\s*["']([^"']+)["'][^>]*>`,
		`(?i)<iframe[^>]+src\s*=\s*["']([^"']+)["'][^>]*>`,
		`(?i)<form[^>]+action\s*=\s*["']([^"']+)["'][^>]*>`,
		`(?i)<object[^>]+data\s*=\s*["']([^"']+)["'][^>]*>`,
		`(?i)<embed[^>]+src\s*=\s*["']([^"']+)["'][^>]*>`,
		`(?i)<source[^>]+src\s*=\s*["']([^"']+)["'][^>]*>`,
		`(?i)<param[^>]+value\s*=\s*["']([^"']+)["'][^>]*>`,
		`(?i)https?://[^\s"'<>]+`,
	}
	regexes := make([]*regexp.Regexp, 0, len(urlPatterns))
	for _, p := range urlPatterns { regexes = append(regexes, regexp.MustCompile(p)) }

	skipExt := func(p string) bool {
		p = strings.ToLower(p)
		for _, ext := range []string{ ".css", ".js", ".png", ".jpg", ".jpeg", ".gif", ".ico", ".svg", ".woff", ".woff2", ".ttf", ".eot" } {
			if strings.HasSuffix(p, ext) { return true }
		}
		return false
	}

	normalize := func(u *url.URL) string {
		// strip fragments
		u.Fragment = ""
		return u.String()
	}

	resolve := func(raw string, page *url.URL) string {
		raw = strings.TrimSpace(raw)
		if raw == "" { return "" }
		if strings.HasPrefix(raw, "javascript:") || strings.HasPrefix(raw, "data:") || strings.HasPrefix(raw, "#") {
			return ""
		}
		rel, err := url.Parse(raw)
		if err != nil { return "" }
		abs := page.ResolveReference(rel)
		if abs.Host != baseHost { return "" }
		if abs.Scheme != "http" && abs.Scheme != "https" { return "" }
		if skipExt(abs.Path) { return "" }
		return normalize(abs)
	}

	worker := func() {
		defer wg.Done()
		for it := range jobs {
			pending.Done()
			if it.d > maxDepth { continue }
			pageURL := it.u

			mu.Lock()
			if visited[pageURL] { mu.Unlock(); continue }
			visited[pageURL] = true
			results[pageURL] = true
			mu.Unlock()

			req, err := http.NewRequest(http.MethodGet, pageURL, nil)
			if err != nil { continue }
			resp, err := client.Do(req)
			if err != nil { continue }
			status := resp.StatusCode
			if status == http.StatusNotFound {
				resp.Body.Close()
				continue
			}
			builder := new(strings.Builder)
			buf := make([]byte, 16384)
			for {
				n, er := resp.Body.Read(buf)
				if n > 0 { builder.Write(buf[:n]) }
				if er != nil { break }
				if builder.Len() > 2*1024*1024 { break }
			}
			resp.Body.Close()

			page, _ := url.Parse(pageURL)
			body := builder.String()
			for _, re := range regexes {
				matches := re.FindAllStringSubmatch(body, -1)
				for _, m := range matches {
					candidate := ""
					if len(m) >= 2 { candidate = m[1] } else if len(m) == 1 { candidate = m[0] }
					abs := resolve(candidate, page)
					if abs == "" { continue }
					mu.Lock()
					if !results[abs] && len(results) < maxPages {
						results[abs] = true
						pending.Add(1)
						jobs <- item{u: abs, d: it.d + 1}
					}
					mu.Unlock()
				}
			}
		}
	}

	for i := 0; i < 200; i++ {
		wg.Add(1)
		go worker()
	}

	pending.Add(1)
	jobs <- item{u: startURL, d: 0}
	go func() { pending.Wait(); close(jobs) }()
	wg.Wait()

	out := make([]string, 0, len(results))
	for u := range results { out = append(out, u) }
	return out, nil
}
