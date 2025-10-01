# PathSeeker - Advanced Web Crawler & Directory Bruteforcer

A high-performance directory bruteforcer written in Go, featuring intelligent error tolerance, content-based deduplication, and automatic wordlist generation through web crawling.

## 🚀 Features

- **Intelligent Error Tolerance**: Continue scanning through error responses with configurable tolerance levels
- **Content-Based Deduplication**: Automatically detect and skip duplicate content to avoid redundant scanning
- **Auto Wordlist Generation**: Crawl target domains to generate custom wordlists from discovered endpoints
- **High Performance**: Optimized HTTP transport with configurable concurrency for maximum speed
- **Progress Tracking**: Real-time progress bar with hit counter
- **Flexible Output**: Clean URL-only output or save to file

## 📋 Installation

### Using Go Install (Recommended)
```bash
go install -v github.com/cybertron10/PathSeeker@latest
```

### Manual Installation
```bash
git clone https://github.com/cybertron10/PathSeeker.git
cd PathSeeker
go build -o pathseeker .
```

## 🎯 Usage

### Basic Single-Level Scan
```bash
./pathseeker -u https://target.com/ -w wordlist.txt -c 200 -o results.txt
```

### Recursive Scan with Error Tolerance
```bash
./pathseeker -u https://target.com/ -w wordlist.txt -r -e 2 -c 200 -o results.txt
```

### Auto-Generate Wordlist and Scan
```bash
./pathseeker -u https://target.com/ -r -e 2 -c 200 -o results.txt
```

## 🏷️ Flags

| Flag | Description | Default | Example |
|------|-------------|---------|---------|
| `-u` | Base URL to scan | Required | `-u https://target.com/` |
| `-w` | Wordlist file path | Auto-generate | `-w wordlist.txt` |
| `-r` | Enable recursive scanning | `false` | `-r` |
| `-e` | Error tolerance depth | `1` | `-e 2` |
| `-c` | Concurrent workers | `50` | `-c 200` |
| `-o` | Output file path | stdout | `-o results.txt` |
| `-se` | Status codes to exclude | `404` | `-se "404,400"` |
| `--crawl-only` | Only crawl and print URLs | `false` | `--crawl-only` |

## 🔧 How It Works

### Error Tolerance System

The `-e` flag controls how many consecutive non-200 responses are allowed before stopping recursion:

- **`-e 1`** (default): Stop on first non-200 response
- **`-e 2`**: Allow 1 consecutive non-200, then stop
- **`-e 3`**: Allow 2 consecutive non-200s, then stop

**Example with `-e 2`:**
```
/reflected/parameter → 500 (error count: 1) → Continue
/reflected/parameter/body → 200 (error count: 0) → Continue  
/reflected/parameter/body/test → 200 (same content) → Stop (content deduplication)
```

### Content-Based Deduplication

Godir automatically detects duplicate content by hashing response bodies. When multiple URLs return identical content, only the shortest path is kept and further recursion is stopped to avoid redundant scanning.

### Auto Wordlist Generation

When no wordlist is provided, Godir automatically:
1. Crawls the target domain (depth 10)
2. Extracts path segments and query parameters
3. Tokenizes camelCase strings
4. Generates a custom wordlist
5. Saves it as `wordlist.txt`

## 🕷️ Intelligent Crawling

PathSeeker features an advanced web crawler that automatically discovers and analyzes target applications to generate custom wordlists.

### How Crawling Works

1. **Initial Discovery**: Starts from the provided base URL
2. **Link Extraction**: Analyzes HTML content to find:
   - Standard links (`<a href="">`)
   - Form actions (`<form action="">`)
   - JavaScript URLs and API endpoints
   - CSS imports and image sources
   - Meta tags and redirects
3. **Domain Filtering**: Only follows links within the same domain
4. **Depth Control**: Configurable crawl depth (default: 10 levels)
5. **Content Analysis**: Extracts meaningful tokens from discovered URLs
6. **Wordlist Generation**: Creates custom wordlists from:
   - Path segments (`/admin/users` → `admin`, `users`)
   - Query parameters (`?id=123&name=test` → `id`, `name`)
   - CamelCase tokenization (`userProfile` → `user`, `profile`)

### Crawling Features

- **Concurrent Processing**: 200 workers for fast crawling
- **Smart Filtering**: Ignores common file extensions and irrelevant content
- **Memory Efficient**: Streams content to avoid memory issues
- **Error Resilient**: Continues crawling despite individual page failures
- **Deduplication**: Removes duplicate URLs automatically

### Example Crawling Output

```
Auto-generating wordlist via crawl (depth 10)...
Crawl discovered 45 URLs; generated 127 words
```

The crawler automatically saves discovered URLs to `wordlist.txt` for future use.

## 🎨 What Makes PathSeeker Unique

### 1. **Intelligent Error Handling**
Unlike traditional scanners that stop on 404s, PathSeeker continues through error responses with configurable tolerance. This is crucial for modern web applications where:
- 500 errors might indicate valid endpoints with server issues
- 403 responses could be authentication-required endpoints
- 401 responses might be protected resources worth noting

### 2. **Content-Aware Scanning**
Most scanners treat each URL independently, leading to:
- Redundant scanning of identical content
- Wasted time on duplicate pages
- Cluttered output with meaningless variations

PathSeeker's content hashing prevents this by:
- Detecting identical responses across different URLs
- Keeping only the shortest path to duplicate content
- Stopping recursion on content matches

### 3. **Adaptive Wordlist Generation**
Traditional scanners rely on static wordlists that may not match the target's structure. PathSeeker generates custom wordlists by:
- Analyzing the target's actual URL structure
- Extracting meaningful tokens from discovered endpoints
- Creating wordlists tailored to the specific application

### 4. **Performance Optimized**
Built with Go's concurrency model and optimized HTTP transport:
- Configurable worker pools
- Connection reuse and keep-alive
- Efficient memory usage
- Real-time progress tracking

## 📊 Example Output

```
Loaded 92 words from wordlist.txt
Scanning with 92 words; mode=recursive; error-tolerance=2; concurrency=200; exclude=404
Progress: [██████████████████████████████████████████████████] 100.0% (184/184) | Hits: 35
Scan complete; 35 hits

https://target.com/admin
https://target.com/api/users
https://target.com/backup/config
https://target.com/reflected/parameter/body
```

## 🔍 Use Cases

- **Web Application Security Testing**: Discover hidden directories and endpoints
- **Bug Bounty Hunting**: Find overlooked attack surfaces
- **Penetration Testing**: Comprehensive directory enumeration
- **Asset Discovery**: Map application structure and functionality

## ⚡ Performance Tips

- Start with `-c 50` and increase based on target response
- Use `-e 1` for initial reconnaissance, then `-e 2` for deeper scans
- Combine with `-se "404,400"` to focus on interesting responses
- Use `-r` only when you need comprehensive coverage

## 🤝 Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## 📄 License

This project is licensed under the MIT License - see the LICENSE file for details.

## ⚠️ Disclaimer

This tool is for educational and authorized testing purposes only. Always ensure you have permission before scanning any target.
