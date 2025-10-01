package main

import (
	"bufio"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"godir/internal/crawler"
	"godir/internal/wordgen"
)

// task represents a URL to request with its current recursion depth
type task struct {
	base   string
	depth  int
	prefix string
}

func joinURL(base, prefix, word string) (string, error) {
	u, err := url.Parse(base)
	if err != nil { return "", err }
	p := strings.TrimSuffix(u.Path, "/")
	joined := path.Join(p+"/", prefix, word)
	if !strings.HasSuffix(joined, "/") {
		joined += "/"
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

	flag.StringVar(&base, "u", "", "Base URL, e.g. http://127.0.0.1/")
	flag.IntVar(&maxDepth, "d", 1, "Recursion depth (0 = just base)")
	flag.IntVar(&concurrency, "c", 50, "Concurrent workers")
	flag.StringVar(&outPath, "o", "", "Output file (optional)")
	flag.StringVar(&wordlistPath, "w", "", "Wordlist file; omit value (-w) to auto-generate from crawl")
	flag.BoolVar(&crawlOnly, "crawl-only", false, "Crawl domain and print URLs only")
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
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for _, u := range urls {
			fmt.Println(u)
		}
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
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		generated := wordgen.FromURLs(urls)
		fmt.Fprintf(os.Stderr, "Crawl discovered %d URLs; generated %d words\n", len(urls), len(generated))
		if len(generated) == 0 {
			fmt.Fprintln(os.Stderr, "auto-generation produced no words")
			os.Exit(1)
		}
		words = generated
		_ = saveWordsToFile(generated, "wordlist.txt")
	}

	if len(words) == 0 {
		fmt.Fprintln(os.Stderr, "no words provided")
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Scanning with %d words; depth=%d; concurrency=%d\n", len(words), maxDepth, concurrency)

	client := &http.Client{ Timeout: 10 * time.Second }

	jobs := make(chan task, concurrency*4)
	seen := sync.Map{}
	wg := &sync.WaitGroup{}
	pending := &sync.WaitGroup{}
	var hits int64

	printLine := func(s string) {
		writer.WriteString(s)
		writer.WriteString("\n")
		if fileWriter != nil {
			fileWriter.WriteString(s)
			fileWriter.WriteString("\n")
		}
	}

	worker := func() {
		defer wg.Done()
		for j := range jobs {
			func(j task) {
				defer pending.Done()
				for _, w := range words {
					u, err := joinURL(j.base, j.prefix, w)
					if err != nil { continue }
					if _, loaded := seen.LoadOrStore(u, struct{}{}); loaded { continue }
					req, err := http.NewRequest(http.MethodGet, u, nil)
					if err != nil { continue }
					resp, err := client.Do(req)
					if err != nil { continue }
					resp.Body.Close()
					if resp.StatusCode >= 200 && resp.StatusCode < 400 {
						atomic.AddInt64(&hits, 1)
						printLine(fmt.Sprintf("%d %s", resp.StatusCode, u))
						if j.depth < maxDepth {
							pending.Add(1)
							jobs <- task{base: j.base, depth: j.depth + 1, prefix: path.Join(j.prefix, w)}
						}
					}
				}
			}(j)
		}
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go worker()
	}

	// seed with base and empty prefix at depth 0
	pending.Add(1)
	jobs <- task{base: base, depth: 0, prefix: ""}

	// closer waits for all pending tasks to finish, then closes jobs so workers exit
	go func() { pending.Wait(); close(jobs) }()

	wg.Wait()
	fmt.Fprintf(os.Stderr, "Scan complete; %d hits\n", atomic.LoadInt64(&hits))
}
