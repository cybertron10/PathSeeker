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

	"github.com/cybertron10/PathSeeker/internal/crawler"
	"github.com/cybertron10/PathSeeker/internal/wordgen"
	"log"
)

// request task represents a single URL attempt and potential recursion
 type reqTask struct {
	base     string
	prefix   string
	word     string
	depth    int
	withSlash bool
	errorCount int // track consecutive non-200 responses
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
	var recursive bool
	var debug bool

	flag.StringVar(&base, "u", "", "Base URL, e.g. http://127.0.0.1/")
	flag.IntVar(&maxDepth, "e", 1, "Error tolerance depth: 1=stop on non-200, 2=allow 1 error level, 3=allow 2 error levels")
	flag.IntVar(&concurrency, "c", 50, "Concurrent workers")
	flag.StringVar(&outPath, "o", "", "Output file (optional)")
	flag.StringVar(&wordlistPath, "w", "", "Wordlist file; omit value (-w) to auto-generate from crawl")
	flag.BoolVar(&crawlOnly, "crawl-only", false, "Crawl domain and print URLs only")
	flag.StringVar(&statusExcludeStr, "se", "404", "Status codes to exclude (comma/space-separated)")
	flag.BoolVar(&recursive, "r", false, "Enable recursive scanning (continue fuzzing until error tolerance is reached)")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")
	flag.CommandLine.Parse(filteredArgs)

	if base == "" {
		fmt.Fprintln(os.Stderr, "-u URL is required")
		os.Exit(1)
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	baseURLParsed, _ := url.Parse(base)
	basePath := baseURLParsed.Path

	// Crawl-only mode: just crawl and print URLs, then exit
	if crawlOnly {
		fmt.Fprintln(os.Stderr, "Crawling domain (depth 10)...")
		urls, err := crawler.Crawl(base, 10, 20000, debug)
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
		urls, err := crawler.Crawl(base, 10, 2000, debug)
		if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
		generated := wordgen.FromURLs(urls, debug)
		fmt.Fprintf(os.Stderr, "Crawl discovered %d URLs; generated %d words\n", len(urls), len(generated))
		if len(generated) == 0 { fmt.Fprintln(os.Stderr, "auto-generation produced no words"); os.Exit(1) }
		words = generated
		_ = saveWordsToFile(generated, "wordlist.txt")
	}

	if len(words) == 0 { fmt.Fprintln(os.Stderr, "no words provided"); os.Exit(1) }

	excluded := parseExcluded(statusExcludeStr)
	scanMode := "single-level"
	if recursive {
		scanMode = "recursive"
	}
	fmt.Fprintf(os.Stderr, "Scanning with %d words; mode=%s; error-tolerance=%d; concurrency=%d; exclude=%s\n", len(words), scanMode, maxDepth, concurrency, statusExcludeStr)

	if debug {
		log.Printf("DEBUG: Configuration - base: %s, maxDepth: %d, concurrency: %d, recursive: %t", base, maxDepth, concurrency, recursive)
		log.Printf("DEBUG: Wordlist: %d words, excluded statuses: %v", len(words), excluded)
	}

	transport := &http.Transport{
		MaxIdleConns:        10000,
		MaxIdleConnsPerHost: concurrency * 2,
		MaxConnsPerHost:     concurrency * 4,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
		DialContext:         (&net.Dialer{ Timeout: 30 * time.Second }).DialContext,
	}
	client := &http.Client{ Transport: transport, Timeout: 10 * time.Second }

	// Memory management: limit queue size to prevent exponential growth
	maxQueueSize := concurrency * 500 // Allow reasonable depth but prevent explosion
	reqJobs := make(chan reqTask, maxQueueSize)
	seen := sync.Map{}
	wg := &sync.WaitGroup{}
	pending := &sync.WaitGroup{}
	var hits int64
	var completed int64
	var totalTasks int64
	var droppedTasks int64 // Track tasks dropped due to queue limit

	// dedupe identical content by shortest path; hash -> bestPath (scoped per first-segment branch)
	hashBest := make(map[string]string)
	hashMu := &sync.Mutex{}

	// track content hashes to detect infinite loops (content that repeats at deeper levels)
	contentAncestors := make(map[string]map[string]bool) // contentHash -> set of paths where this content was seen
	contentAncestorsMu := &sync.Mutex{}

	// store only 200s for final output (normalized, unique)
	// final200 := make(map[string]struct{})
	// finalMu := &sync.Mutex{}

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
		if debug {
			log.Printf("DEBUG: Request %s -> %d (hash: %s)", fullURL, code, sum)
		}
		if _, skip := excluded[code]; !skip {
			atomic.AddInt64(&hits, 1)
			return code, sum, true
		}
		return code, sum, false
	}

	// Check if content hash creates an infinite loop
	checkInfiniteLoop := func(contentHash, currentPath string) bool {
		if contentHash == "" {
			return false
		}
		
		contentAncestorsMu.Lock()
		defer contentAncestorsMu.Unlock()
		
		// Check if this content hash was seen anywhere in our known paths
		if paths, exists := contentAncestors[contentHash]; exists {
			for knownPath := range paths {
				// Skip if it's the exact same path (avoid self-comparison)
				if knownPath == currentPath {
					continue
				}
				
				// Check if content was seen in a parent or child path
				// Use "/" suffix to ensure we're checking actual path hierarchy
				if strings.HasPrefix(currentPath+"/", knownPath+"/") || strings.HasPrefix(knownPath+"/", currentPath+"/") {
					if debug {
						log.Printf("DEBUG: Infinite loop detected - content %s already seen at path %s (current: %s)", contentHash, knownPath, currentPath)
					}
					return true
				}
			}
		}
		return false
	}
	
	// Record that a path has specific content (only call after deciding to recurse)
	recordPathContent := func(contentHash, currentPath string) {
		if contentHash == "" {
			return
		}
		contentAncestorsMu.Lock()
		defer contentAncestorsMu.Unlock()
		
		if contentAncestors[contentHash] == nil {
			contentAncestors[contentHash] = make(map[string]bool)
		}
		contentAncestors[contentHash][currentPath] = true
		if debug {
			log.Printf("DEBUG: Recorded path %s with content hash %s", currentPath, contentHash)
		}
	}

	// Pre-check function to detect reflective endpoints at any level
	preCheckReflective := func(baseURL, prefix string) bool {
		if debug {
			checkPath := path.Join(prefix)
			if checkPath == "" {
				checkPath = "root"
			}
			log.Printf("DEBUG: Pre-checking path '%s' for reflective endpoint", checkPath)
		}
		
		testWords := []string{"test123xyz", "random456abc", "check789def"}
		type testResult struct {
			hash   string
			status int
		}
		testResults := make([]testResult, 0, len(testWords))
		
		for _, testWord := range testWords {
			testURL, err := buildURL(baseURL, prefix, testWord, false)
			if err != nil {
				continue
			}
			
			testReq, err := http.NewRequest(http.MethodGet, testURL, nil)
			if err != nil {
				continue
			}
			
			testResp, err := client.Do(testReq)
			if err != nil {
				continue
			}
			
			status := testResp.StatusCode
			var testHash string
			
			// Hash content for ALL responses (not just 200s)
			if status != 404 {
				lr := io.LimitReader(testResp.Body, 256*1024)
				h := sha1.New()
				io.Copy(h, lr)
				testHash = fmt.Sprintf("%x", h.Sum(nil))
			}
			testResp.Body.Close()
			
			testResults = append(testResults, testResult{hash: testHash, status: status})
			
			if debug {
				log.Printf("DEBUG: Pre-scan %s returned status %d, hash %s", testURL, status, testHash)
			}
		}
		
	// Check if all test words return the same response (status + content)
	if len(testResults) >= 2 {
		allSame := true
		firstResult := testResults[0]
		
		// Skip pre-check if all are 404s (expected for non-existent paths)
		if firstResult.status == 404 {
			if debug {
				log.Printf("DEBUG: Pre-check skipped - all test paths return 404 (expected behavior)")
			}
			return false
		}
		
		for _, result := range testResults[1:] {
			// Compare both status code and content hash
			if result.status != firstResult.status || result.hash != firstResult.hash {
				allSame = false
				break
			}
		}
		
		if allSame {
			pathDesc := prefix
			if pathDesc == "" {
				pathDesc = "root"
			}
			fmt.Fprintf(os.Stderr, "\n⚠️  REFLECTIVE ENDPOINT at '%s': All test paths return identical response (status: %d, hash: %s)\n", pathDesc, firstResult.status, firstResult.hash)
			
			if debug {
				log.Printf("DEBUG: Reflective endpoint detected at path '%s' (status %d) - blocking recursion", pathDesc, firstResult.status)
			}
			return true
		}
	}
	
	return false
}

	// Progress bar function
	updateProgress := func() {
		completed := atomic.LoadInt64(&completed)
		total := atomic.LoadInt64(&totalTasks)
		hits := atomic.LoadInt64(&hits)
		
		if total > 0 {
			percent := float64(completed) / float64(total) * 100
			barWidth := 50
			filled := int(percent / 100 * float64(barWidth))
			
			bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
			fmt.Fprintf(os.Stderr, "\rProgress: [%s] %.1f%% (%d/%d) | Hits: %d", bar, percent, completed, total, hits)
		}
	}

	worker := func() {
		defer wg.Done()
		for t := range reqJobs {
			func(t reqTask) {
				defer pending.Done()
				defer atomic.AddInt64(&completed, 1)
				
				u, err := buildURL(t.base, t.prefix, t.word, t.withSlash)
				if err != nil { return }
				if debug {
					log.Printf("DEBUG: Built URL %s from task %+v", u, t)
				}
			code, sum, ok := requestURL(u)
			if !ok { return }
			
			// Record all 200 responses with their content hash for loop detection
			if code == 200 && sum != "" {
				parsedURL, err := url.Parse(u)
				if err == nil {
					currentPath := parsedURL.Path
					// Always record 200 responses so future requests can detect loops
					recordPathContent(sum, currentPath)
				}
			}
			
		// Recursion logic: -r flag controls whether to recurse
		if !t.withSlash && recursive {
					_, skip := excluded[code]
					if !skip {
						// Calculate new error count
						newErrorCount := t.errorCount
						if code != 200 {
							newErrorCount++
						} else {
							newErrorCount = 0 // reset on 200
						}
						
						// Check if we should recurse based on error tolerance
						shouldRecurse := newErrorCount < maxDepth
						
						// Apply content-hash pruning for 200s to avoid duplicate content
						if code == 200 && shouldRecurse {
							// determine branch key (first segment under base path)
							norm := normalizeOutputURL(u)
							pu, perr := url.Parse(norm)
							branch := ""
							if perr == nil {
								rel := strings.TrimPrefix(pu.Path, basePath)
								rel = strings.TrimPrefix(rel, "/")
								if rel != "" {
									parts := strings.SplitN(rel, "/", 2)
									branch = parts[0]
								}
							}
							key := branch + "|" + sum
							hashMu.Lock()
							best, exists := hashBest[key]
							if !exists || len(norm) < len(best) {
								hashBest[key] = norm
								best = norm
							}
							if debug && exists && len(norm) < len(best) {
								log.Printf("DEBUG: Found shorter path %s (was %s) for hash %s", norm, best, sum)
							}
							shouldRecurse = (best == norm)
							if debug && code == 200 {
								if !shouldRecurse {
									log.Printf("DEBUG: Skipping recursion for %s (duplicate content)", norm)
								} else {
									log.Printf("DEBUG: Content %s is unique, proceeding with recursion", norm)
								}
							}
							hashMu.Unlock()
						}
						
					// Check for infinite content loops before recursion
					if code == 200 && sum != "" && shouldRecurse {
						// Parse URL to get current path
						parsedURL, err := url.Parse(u)
						if err == nil {
							currentPath := parsedURL.Path
							
							// Check if this content creates an infinite loop
							if checkInfiniteLoop(sum, currentPath) {
								shouldRecurse = false
								if debug {
									log.Printf("DEBUG: Blocked recursion for %s due to infinite loop", u)
								}
							}
						}
					}
						if debug {
							log.Printf("DEBUG: URL %s -> code %d, errorCount %d, shouldRecurse %t", u, code, newErrorCount, shouldRecurse)
						}

					if shouldRecurse {
						nextPrefix := path.Join(t.prefix, t.word)
						
						// Pre-check at this directory level before recursing
						if preCheckReflective(t.base, nextPrefix) {
							// Reflective endpoint detected at this level, skip recursion
							if debug {
								log.Printf("DEBUG: Skipping recursion into %s (reflective endpoint)", nextPrefix)
							}
					} else {
						// Not reflective, proceed with recursion
						add := len(words)
						
						// Memory management: only add tasks if queue has space
						queueLen := len(reqJobs)
						availableSpace := maxQueueSize - queueLen
						
						if availableSpace < add {
							// Queue is near full, skip this recursion level to prevent memory explosion
							dropped := add
							atomic.AddInt64(&droppedTasks, int64(dropped))
							if debug {
								log.Printf("DEBUG: Skipping recursion into %s - queue near full (%d/%d tasks, would add %d)", nextPrefix, queueLen, maxQueueSize, add)
							}
						} else {
							// Queue has space, add tasks
							pending.Add(add)
							atomic.AddInt64(&totalTasks, int64(add))
							if debug {
								log.Printf("DEBUG: Recursing into %s with %d new tasks (queue: %d/%d)", nextPrefix, add, queueLen, maxQueueSize)
							}
							for _, w := range words {
								reqJobs <- reqTask{base: t.base, prefix: nextPrefix, word: w, depth: t.depth + 1, withSlash: false, errorCount: newErrorCount}
							}
						}
					}
					}
					}
				}
			}(t)
		}
	}

	for i := 0; i < concurrency; i++ { wg.Add(1); go worker() }

	// Pre-check at root level
	if preCheckReflective(base, "") {
		fmt.Fprintf(os.Stderr, "This endpoint appears to return the same response regardless of the path.\n")
		fmt.Fprintf(os.Stderr, "Skipping scan to avoid infinite false positives.\n\n")
		return
	}

	// seed: all words at root, without trailing slash only
	seedTasks := len(words)
	pending.Add(seedTasks)
	atomic.StoreInt64(&totalTasks, int64(seedTasks))
	for _, w := range words {
		reqJobs <- reqTask{base: base, prefix: "", word: w, depth: 0, withSlash: false, errorCount: 0}
	}

	// Start progress updater
	progressTicker := time.NewTicker(100 * time.Millisecond)
	defer progressTicker.Stop()
	go func() {
		for range progressTicker.C {
			updateProgress()
		}
	}()

	go func() { pending.Wait(); close(reqJobs) }()
	wg.Wait()
	
	// Final progress update
	updateProgress()
	fmt.Fprintln(os.Stderr) // New line after progress bar

	// emit only 200s at the end based on content hashes (union across branches)
	hashMu.Lock()
	seenOut := make(map[string]struct{})
	for _, u := range hashBest {
		if _, ok := seenOut[u]; ok { continue }
		seenOut[u] = struct{}{}
		writer.WriteString(u)
		writer.WriteString("\n")
		if fileWriter != nil { fileWriter.WriteString(u); fileWriter.WriteString("\n") }
	}
	hashMu.Unlock()

	fmt.Fprintf(os.Stderr, "Scan complete; %d hits\n", atomic.LoadInt64(&hits))
	
	// Report dropped tasks if any
	dropped := atomic.LoadInt64(&droppedTasks)
	if dropped > 0 {
		fmt.Fprintf(os.Stderr, "⚠️  Note: %d tasks were dropped due to queue limits (prevents memory explosion)\n", dropped)
		fmt.Fprintf(os.Stderr, "Tip: Reduce wordlist size or error tolerance (-e) for deeper scans\n")
	}
}
