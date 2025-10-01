package main

import (
	"bufio"
	"crypto/sha1"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"godir/internal/crawler"
	"godir/internal/wordgen"
)

// request task represents a single URL attempt and potential recursion
 type reqTask struct {
	base     string
	prefix   string
	word     string
	depth    int
	withSlash bool
}

func buildURL(base, prefix, word string, withSlash bool) (string, error) {
	u, err := url.Parse(base)
	if err != nil { return "", err }
	p := strings.TrimSuffix(u.Path, "/")
	joined := path.Join(p+"/", prefix, word)
	if withSlash {
		if !strings.HasSuffix(joined, "/") { joined += "/" }
	} else {
		joined = strings.TrimSuffix(joined, "/")
	}
	u.Path = joined
	return u.String(), nil
}

// preparseArgs detects "-w" passed without a value and marks autoGenerate
func preparseArgs(args []string) (filtered []string, autoGenerate bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-w" {
			// if last or next looks like a flag, treat as auto
			if i == len(args)-1 || strings.HasPrefix(args[i+1], "-") {
				autoGenerate = true
				continue // drop the bare -w
			}
		}
		filtered = append(filtered, a)
	}
	return
}

func loadWordsFromFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil { return nil, err }
	defer f.Close()
	s := bufio.NewScanner(f)
	words := make([]string, 0, 1024)
	for s.Scan() {
		w := strings.TrimSpace(s.Text())
		if w == "" || strings.HasPrefix(w, "#") { continue }
		w = strings.TrimPrefix(w, "/")
		words = append(words, w)
	}
	if err := s.Err(); err != nil { return nil, err }
	return words, nil
}

func saveWordsToFile(words []string, outPath string) error {
	f, err := os.Create(outPath)
	if err != nil { return err }
	defer f.Close()
	bw := bufio.NewWriterSize(f, 64*1024)
	for _, w := range words {
		bw.WriteString(w)
		bw.WriteByte('\n')
	}
	return bw.Flush()
}

func parseExcluded(statuses string) map[int]struct{} {
	set := map[int]struct{}{}
	if statuses == "" { return set }
	split := strings.FieldsFunc(statuses, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	for _, s := range split {
		s = strings.TrimSpace(s)
		if s == "" { continue }
		if n, err := strconv.Atoi(s); err == nil { set[n] = struct{}{} }
	}
	return set
}

func normalizeOutputURL(u string) string {
	if strings.HasSuffix(u, "/") {
		if !strings.HasSuffix(u, "://") {
			return strings.TrimRight(u, "/")
		}
	}
	return u
}

func main() {
	// Pre-parse to allow bare -w (no value) to trigger auto generation (we just drop it)
	filteredArgs, _ := preparseArgs(os.Args[1:])
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	var base string
	var maxDepth int
	var concurrency int
	var outPath string
	var wordlistPath string
	var crawlOnly bool
	var statusExcludeStr string

	flag.StringVar(&base, "u", "", "Base URL, e.g. http://127.0.0.1/")
	flag.IntVar(&maxDepth, "d", 1, "Recursion depth (0 = just base)")
	flag.IntVar(&concurrency, "c", 50, "Concurrent workers")
	flag.StringVar(&outPath, "o", "", "Output file (optional)")
	flag.StringVar(&wordlistPath, "w", "", "Wordlist file; omit value (-w) to auto-generate from crawl")
	flag.BoolVar(&crawlOnly, "crawl-only", false, "Crawl domain and print URLs only")
	flag.StringVar(&statusExcludeStr, "se", "404", "Status codes to exclude (comma/space-separated)")
	flag.CommandLine.Parse(filteredArgs)

	if base == "" {
		fmt.Fprintln(os.Stderr, "-u URL is required")
		os.Exit(1)
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}

	// Crawl-only mode: just crawl and print URLs, then exit
	if crawlOnly {
		fmt.Fprintln(os.Stderr, "Crawling domain (depth 10)...")
		urls, err := crawler.Crawl(base, 10, 20000)
		if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
		for _, u := range urls { fmt.Println(u) }
		fmt.Fprintf(os.Stderr, "Crawled %d URLs\n", len(urls))
		return
	}

	var out *os.File
	var err error
	if outPath != "" {
		out, err = os.Create(outPath)
		if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
		defer out.Close()
	}

	writer := bufio.NewWriterSize(os.Stdout, 64*1024)
	defer writer.Flush()
	fileWriter := (*bufio.Writer)(nil)
	if out != nil {
		fileWriter = bufio.NewWriterSize(out, 64*1024)
		defer fileWriter.Flush()
	}

	// Resolve wordlist source: if -w has a value, load it; otherwise crawl and generate (no stdin fallback)
	var words []string
	if wordlistPath != "" {
		w, err := loadWordsFromFile(wordlistPath)
		if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
		fmt.Fprintf(os.Stderr, "Loaded %d words from %s\n", len(w), wordlistPath)
		words = w
	} else {
		fmt.Fprintln(os.Stderr, "Auto-generating wordlist via crawl (depth 10)...")
		urls, err := crawler.Crawl(base, 10, 2000)
		if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
		generated := wordgen.FromURLs(urls)
		fmt.Fprintf(os.Stderr, "Crawl discovered %d URLs; generated %d words\n", len(urls), len(generated))
		if len(generated) == 0 { fmt.Fprintln(os.Stderr, "auto-generation produced no words"); os.Exit(1) }
		words = generated
		_ = saveWordsToFile(generated, "wordlist.txt")
	}

	if len(words) == 0 { fmt.Fprintln(os.Stderr, "no words provided"); os.Exit(1) }

	excluded := parseExcluded(statusExcludeStr)
	fmt.Fprintf(os.Stderr, "Scanning with %d words; depth=%d; concurrency=%d; exclude=%s\n", len(words), maxDepth, concurrency, statusExcludeStr)

	transport := &http.Transport{
		MaxIdleConns:        10000,
		MaxIdleConnsPerHost: concurrency * 2,
		MaxConnsPerHost:     concurrency * 4,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
		DialContext:         (&net.Dialer{ Timeout: 30 * time.Second }).DialContext,
	}
	client := &http.Client{ Transport: transport, Timeout: 10 * time.Second }

	reqJobs := make(chan reqTask, concurrency*100)
	seen := sync.Map{}
	wg := &sync.WaitGroup{}
	pending := &sync.WaitGroup{}
	var hits int64

	// dedupe identical content by shortest path; hash -> bestPath
	hashBest := make(map[string]string)
	hashMu := &sync.Mutex{}

	// store only 200s for final output (normalized, unique)
	final200 := make(map[string]struct{})
	finalMu := &sync.Mutex{}

	requestURL := func(fullURL string) (int, string, bool) {
		if _, loaded := seen.LoadOrStore(fullURL, struct{}{}); loaded { return 0, "", false }
		req, err := http.NewRequest(http.MethodGet, fullURL, nil)
		if err != nil { return 0, "", false }
		req.Header.Set("Connection", "keep-alive")
		resp, err := client.Do(req)
		if err != nil { return 0, "", false }
		code := resp.StatusCode
		var sum string
		if code == 200 {
			lr := io.LimitReader(resp.Body, 256*1024)
			h := sha1.New()
			io.Copy(h, lr)
			sum = fmt.Sprintf("%x", h.Sum(nil))
		}
		resp.Body.Close()
		if _, skip := excluded[code]; !skip {
			atomic.AddInt64(&hits, 1)
			if code == 200 {
				finalMu.Lock()
				final200[normalizeOutputURL(fullURL)] = struct{}{}
				finalMu.Unlock()
			}
			return code, sum, true
		}
		return code, sum, false
	}

	worker := func() {
		defer wg.Done()
		for t := range reqJobs {
			func(t reqTask) {
				defer pending.Done()
				u, err := buildURL(t.base, t.prefix, t.word, t.withSlash)
				if err != nil { return }
				code, hash, ok := requestURL(u)
				if !ok { return }
				// prune recursion if same-content already seen at a shorter or equal path
				if t.withSlash && code == 200 && t.depth < maxDepth {
					hashMu.Lock()
					best, exists := hashBest[hash]
					if !exists || len(u) < len(best) {
						hashBest[hash] = u
					}
					shouldRecurse := !exists || len(u) <= len(best)
					hashMu.Unlock()
					if shouldRecurse {
						nextPrefix := path.Join(t.prefix, t.word)
						add := len(words) * 2
						pending.Add(add)
						for _, w := range words {
							reqJobs <- reqTask{base: t.base, prefix: nextPrefix, word: w, depth: t.depth + 1, withSlash: false}
							reqJobs <- reqTask{base: t.base, prefix: nextPrefix, word: w, depth: t.depth + 1, withSlash: true}
						}
					}
				}
			}(t)
		}
	}

	for i := 0; i < concurrency; i++ { wg.Add(1); go worker() }

	// seed: all words at root, both variants
	pending.Add(len(words) * 2)
	for _, w := range words {
		reqJobs <- reqTask{base: base, prefix: "", word: w, depth: 0, withSlash: false}
		reqJobs <- reqTask{base: base, prefix: "", word: w, depth: 0, withSlash: true}
	}

	go func() { pending.Wait(); close(reqJobs) }()
	wg.Wait()

	// emit only 200s at the end
	finalMu.Lock()
	for u := range final200 {
		writer.WriteString("200 ")
		writer.WriteString(u)
		writer.WriteString("\n")
		if fileWriter != nil { fileWriter.WriteString("200 "); fileWriter.WriteString(u); fileWriter.WriteString("\n") }
	}
	finalMu.Unlock()

	fmt.Fprintf(os.Stderr, "Scan complete; %d hits\n", atomic.LoadInt64(&hits))
}
