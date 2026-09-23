package service

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/newo-ether/filo/internal/protocol"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// MaxImageBytes bounds one native image read.
const MaxImageBytes = 64 * 1024 * 1024

// markdownParser is a conforming CommonMark parser; image extraction must not
// be a hand-rolled scanner because code samples and external origins are the
// whole point of the boundary.
var markdownParser = goldmark.New()

func unavailableImage() error {
	return CodexRpcError("The native image is unavailable", -32600)
}

// NativeImageLinks returns the local image destinations of actual Markdown
// image syntax in document order. Code samples, ordinary links, relative paths
// and external or UNC origins grant no file access.
func NativeImageLinks(markdown string) []string {
	document := markdownParser.Parser().Parse(text.NewReader([]byte(markdown)))
	links := make([]string, 0, 1)
	seen := make(map[string]bool)
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		image, isImage := node.(*ast.Image)
		if !isImage || len(image.Destination) == 0 {
			return ast.WalkContinue, nil
		}
		// Normalize the parser destination exactly as rendered CommonMark links.
		// The mobile image map uses this encoded key, not the decoded disk path.
		link := string(util.URLEscape(image.Destination, true))
		if _, local := LocalImagePath(link); !local || seen[link] {
			return ast.WalkContinue, nil
		}
		seen[link] = true
		links = append(links, link)
		return ast.WalkContinue, nil
	})
	return links
}

// LocalImagePath resolves the local file path a Markdown destination or a URI
// native imageView reference may authorize. URI destinations are decoded as
// file URLs, literal paths keep their percent characters, and any non-local,
// UNC or malformed target refuses the read.
//
// Recorded divergence: Node's win32 isAbsolute accepts a drive-less rooted path
// such as "/tmp/x.png"; this implementation requires a real volume and refuses
// it, which only tightens access.
func LocalImagePath(link string) (string, bool) {
	path := ""
	if strings.HasPrefix(link, "file:") {
		parsed, err := url.Parse(link)
		if err != nil || !strings.EqualFold(parsed.Scheme, "file") {
			return "", false
		}
		if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
			return "", false
		}
		if parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", false
		}
		// Parsed.Path is already decoded once, which matches Node's single
		// decodeURIComponent. Node additionally refuses a file URL that hides a
		// separator behind an escape on Windows; mirror that refusal.
		if runtime.GOOS == "windows" && escapesSeparator(parsed.EscapedPath()) {
			return "", false
		}
		path = parsed.Path
	} else {
		decoded, err := url.PathUnescape(link)
		if err != nil {
			return "", false
		}
		path = decoded
	}
	// Desktop Markdown can prefix a Windows drive with a URL-style slash, as in
	// the TS guard /^\/[a-z]:[\\/]/i.
	if runtime.GOOS == "windows" && len(path) >= 4 && path[0] == '/' && isDriveLetter(path[1]) &&
		path[2] == ':' && (path[3] == '/' || path[3] == '\\') {
		path = path[1:]
	}
	if !absoluteLocal(path) {
		return "", false
	}
	return path, true
}

func isDriveLetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

// escapesSeparator reports whether an escaped path encodes '/' or '\', the two
// file-URL paths Node rejects on Windows as ERR_INVALID_FILE_URL_PATH.
func escapesSeparator(escaped string) bool {
	for index := 0; index+2 < len(escaped); index++ {
		if escaped[index] != '%' {
			continue
		}
		high, low := escaped[index+1]|0x20, escaped[index+2]|0x20
		if high == '2' && low == 'f' || high == '5' && low == 'c' {
			return true
		}
	}
	return false
}

// absoluteLocal mirrors the shared path gate: absolute, not UNC, not a
// protocol-relative path and free of NUL bytes.
func absoluteLocal(path string) bool {
	return path != "" && filepath.IsAbs(path) &&
		!strings.HasPrefix(path, `\\`) && !strings.HasPrefix(path, "//") &&
		!strings.ContainsRune(path, 0)
}

// ImageIndex mirrors the request-side selector: only a safe non-negative
// integer addresses an inline image link. Absent selects the native imageView
// reference instead.
func ImageIndex(raw any) (*int, bool) {
	value, isNumber := raw.(float64)
	if !isNumber || !isSafeInteger(value) || value < 0 || value > 1<<31-1 {
		return nil, false
	}
	index := int(value)
	return &index, true
}

func isSafeInteger(value float64) bool {
	return value == float64(int(value)) && value >= -9007199254740991 && value <= 9007199254740991
}

// ServeConversationImage authorizes bytes only through a native imageView
// reference or a parsed image destination on the exact message, then streams a
// bounded, sniffed image. Every refusal uses the same unavailable error so a
// missing or oversized file cannot make the task itself unenterable.
func ServeConversationImage(response http.ResponseWriter, message protocol.Message, imageIndex *int) error {
	reference := ""
	// A literal native path keeps its percent characters; only URI and parsed
	// Markdown destinations are decoded.
	literal := false
	switch {
	case imageIndex == nil:
		if message.Activity == nil || !message.Activity.ImagePath.Known {
			return unavailableImage()
		}
		reference = message.Activity.ImagePath.Value
		literal = !strings.HasPrefix(reference, "file:")
	default:
		if *imageIndex < 0 || *imageIndex >= len(message.ImageLinks) {
			return unavailableImage()
		}
		reference = message.ImageLinks[*imageIndex]
	}
	path := reference
	if !literal {
		local, ok := LocalImagePath(reference)
		if !ok {
			return unavailableImage()
		}
		path = local
	}
	if !absoluteLocal(path) {
		return unavailableImage()
	}
	file, err := os.Open(path)
	if err != nil {
		return unavailableImage()
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 12 || info.Size() > MaxImageBytes {
		return unavailableImage()
	}
	header := make([]byte, 12)
	if _, err := io.ReadFull(file, header); err != nil {
		return unavailableImage()
	}
	mime := imageMIME(header)
	if mime == "" {
		return unavailableImage()
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return unavailableImage()
	}
	response.Header().Set("Content-Type", mime)
	response.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(http.StatusOK)
	if _, err := io.Copy(response, io.LimitReader(file, info.Size())); err != nil {
		return err
	}
	return nil
}

// imageMIME sniffs the container from the first twelve bytes; native Codex
// renders exactly these four families.
func imageMIME(header []byte) string {
	switch {
	case bytes.Equal(header[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}):
		return "image/png"
	case header[0] == 255 && header[1] == 216 && header[2] == 255:
		return "image/jpeg"
	case string(header[:6]) == "GIF87a" || string(header[:6]) == "GIF89a":
		return "image/gif"
	case string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}
