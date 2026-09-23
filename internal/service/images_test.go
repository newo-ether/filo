package service

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
)

const pngHex = "89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c489"

func pngBytes(t *testing.T) []byte {
	t.Helper()
	content, err := hex.DecodeString(pngHex)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

// fileURL renders an absolute path the way desktop Markdown records it, with
// the percent characters of the literal name still encoded.
func fileURL(t *testing.T, path string) string {
	t.Helper()
	parsed := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(path)}
	return parsed.String()
}

func writeFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertUnavailable(t *testing.T, err error) {
	t.Helper()
	var rpc *codex.RpcError
	if !errors.As(err, &rpc) {
		t.Fatalf("error = %v, want the shared unavailable error", err)
	}
	if rpc.Message != "The native image is unavailable" || rpc.Code == nil || *rpc.Code != -32600 {
		t.Fatalf("error = %q/%v, want the shared unavailable error", rpc.Message, rpc.Code)
	}
}

func serveImage(t *testing.T, message protocol.Message, index *int) (*httptest.ResponseRecorder, error) {
	t.Helper()
	response := httptest.NewRecorder()
	err := ServeConversationImage(response, message, index)
	return response, err
}

func TestNativeImageLinksUsesParsedImageSyntax(t *testing.T) {
	path := filepath.Join(t.TempDir(), "\u4e2d\u6587 image.png")
	link := fileURL(t, path)
	document := "![first](<" + link + ">)\n![again][picture]\n\n[picture]: " + link
	links := NativeImageLinks(document)
	if len(links) != 1 || links[0] != link {
		t.Fatalf("links = %q, want exactly [%q]", links, link)
	}
}

func TestNativeImageLinksRefusesLinksSamplesAndRemoteOrigins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.png")
	link := fileURL(t, path)
	document := "[ordinary](" + link + ")\n\n```md\n![sample](" + link + ")\n```\n\n" +
		"![remote](https://example.com/a.png) ![relative](./a.png) ![unc](file://server/share/a.png)"
	if links := NativeImageLinks(document); len(links) != 0 {
		t.Fatalf("links = %q, want none", links)
	}
}

func TestLocalImagePathAcceptsOnlyLocalAbsolutePaths(t *testing.T) {
	local := filepath.Join(t.TempDir(), "\u4e2d\u6587%20 image.png")
	// A literal path keeps its percent characters; only URI and Markdown
	// destinations are decoded, matching the TS split.
	decoded := filepath.Join(filepath.Dir(local), "\u4e2d\u6587  image.png")
	localhost := (&url.URL{Scheme: "file", Host: "localhost", Path: "/" + filepath.ToSlash(local)}).String()
	cases := []struct {
		name   string
		link   string
		want   string
		localB bool
	}{
		{"decodes a markdown destination", local, decoded, true},
		{"file uri", fileURL(t, local), local, true},
		{"localhost authority", localhost, local, true},
		{"unc uri", "file://server/share/image.png", "", false},
		{"escaped separator", "file:///C:/a%2Fb.png", "", false},
		{"escaped backslash", "file:///C:/a%5Cb.png", "", false},
		{"remote uri", "https://example.com/image.png", "", false},
		{"relative literal", `relative\image.png`, "", false},
		{"relative uri", "./image.png", "", false},
		{"protocol relative", "//server/share/image.png", "", false},
		{"unc literal", `\\server\share\image.png`, "", false},
		{"nul byte", "C:/image\x00.png", "", false},
		{"malformed escape", "C:/image%zz.png", "", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := LocalImagePath(testCase.link)
			if ok != testCase.localB {
				t.Fatalf("LocalImagePath(%q) local = %v, want %v", testCase.link, ok, testCase.localB)
			}
			if ok && !strings.EqualFold(filepath.ToSlash(got), filepath.ToSlash(testCase.want)) {
				t.Fatalf("LocalImagePath(%q) = %q, want %q", testCase.link, got, testCase.want)
			}
		})
	}
}

func TestLocalImagePathStripsAUrlStyleDriveSlash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.png")
	slashed := "/" + filepath.ToSlash(path)
	got, ok := LocalImagePath(slashed)
	if !ok {
		t.Fatalf("LocalImagePath(%q) refused a drive-prefixed path", slashed)
	}
	if filepath.ToSlash(got) != filepath.ToSlash(path) {
		t.Fatalf("LocalImagePath(%q) = %q, want %q", slashed, got, path)
	}
}

func TestImageIndexAcceptsOnlySafeNonNegativeIntegers(t *testing.T) {
	accepted := map[float64]int{0: 0, 1: 1, 2147483647: 2147483647}
	for raw, want := range accepted {
		index, ok := ImageIndex(raw)
		if !ok || index == nil || *index != want {
			t.Fatalf("ImageIndex(%v) = %v/%v, want %d", raw, index, ok, want)
		}
	}
	for _, raw := range []any{nil, "0", 0.5, -1.0, float64(2147483648)} {
		if index, ok := ImageIndex(raw); ok {
			t.Fatalf("ImageIndex(%v) = %v, want a refusal", raw, index)
		}
	}
}

func TestServeConversationImageStreamsAnAuthenticatedReference(t *testing.T) {
	content := pngBytes(t)
	path := writeFile(t, "\u4e2d\u6587%20 image.png", content)
	message := protocol.Message{}
	message.Activity = &protocol.Activity{Type: "tool", ImagePath: protocol.Known(path)}
	response, err := serveImage(t, message, nil)
	if err != nil {
		t.Fatalf("serve = %v, want a streamed image", err)
	}
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), content) {
		t.Fatalf("response = %d/%d bytes, want 200/%d", response.Code, response.Body.Len(), len(content))
	}
	header := response.Result().Header
	if got := header.Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
	if got := header.Get("Content-Length"); got != "33" {
		t.Fatalf("Content-Length = %q, want 33", got)
	}
	if got := header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestServeConversationImageAcceptsAFileURIReference(t *testing.T) {
	content := pngBytes(t)
	path := writeFile(t, "image.png", content)
	message := protocol.Message{}
	message.Activity = &protocol.Activity{Type: "tool", ImagePath: protocol.Known(fileURL(t, path))}
	response, err := serveImage(t, message, nil)
	if err != nil || !bytes.Equal(response.Body.Bytes(), content) {
		t.Fatalf("file URI reference = %v/%d bytes, want the image", err, response.Body.Len())
	}
}

func TestServeConversationImageResolvesOnlyTheRequestedInlineIndex(t *testing.T) {
	content := pngBytes(t)
	path := writeFile(t, "image.png", content)
	message := protocol.Message{ImageLinks: []string{fileURL(t, path), "https://example.com/remote.png"}}
	index := 0
	response, err := serveImage(t, message, &index)
	if err != nil || !bytes.Equal(response.Body.Bytes(), content) {
		t.Fatalf("index 0 = %v/%d bytes, want the image", err, response.Body.Len())
	}
	outOfRange := 2
	if _, err := serveImage(t, message, &outOfRange); err == nil {
		t.Fatal("out-of-range index was served")
	}
	negative := -1
	if _, err := serveImage(t, message, &negative); err == nil {
		t.Fatal("negative index was served")
	}
	remote := 1
	_, err = serveImage(t, message, &remote)
	assertUnavailable(t, err)
}

func TestServeConversationImageRefusesUnusableReferences(t *testing.T) {
	content := pngBytes(t)
	image := writeFile(t, "image.png", content)
	plain := writeFile(t, "plain.png", []byte("not an image"))
	short := writeFile(t, "short.png", []byte("tiny"))
	missing := filepath.Join(t.TempDir(), "missing.png")
	oversized := writeFile(t, "oversized.png", content)
	if err := os.Truncate(oversized, MaxImageBytes+1); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	relative := filepath.Base(image)
	cases := []struct {
		name  string
		build func() protocol.Message
		index *int
	}{
		{"no reference at all", func() protocol.Message { return protocol.Message{} }, nil},
		{"absent image path", func() protocol.Message {
			message := protocol.Message{}
			message.Activity = &protocol.Activity{Type: "tool"}
			return message
		}, nil},
		{"unc reference", func() protocol.Message {
			message := protocol.Message{}
			message.Activity = &protocol.Activity{Type: "tool", ImagePath: protocol.Known("file://server/share/image.png")}
			return message
		}, nil},
		{"remote reference", func() protocol.Message {
			message := protocol.Message{}
			message.Activity = &protocol.Activity{Type: "tool", ImagePath: protocol.Known("https://example.com/image.png")}
			return message
		}, nil},
		{"relative reference", func() protocol.Message {
			message := protocol.Message{}
			message.Activity = &protocol.Activity{Type: "tool", ImagePath: protocol.Known(relative)}
			return message
		}, nil},
		{"directory", func() protocol.Message {
			message := protocol.Message{}
			message.Activity = &protocol.Activity{Type: "tool", ImagePath: protocol.Known(directory)}
			return message
		}, nil},
		{"missing file", func() protocol.Message {
			message := protocol.Message{}
			message.Activity = &protocol.Activity{Type: "tool", ImagePath: protocol.Known(missing)}
			return message
		}, nil},
		{"unrecognized bytes", func() protocol.Message {
			message := protocol.Message{}
			message.Activity = &protocol.Activity{Type: "tool", ImagePath: protocol.Known(plain)}
			return message
		}, nil},
		{"shorter than a header", func() protocol.Message {
			message := protocol.Message{}
			message.Activity = &protocol.Activity{Type: "tool", ImagePath: protocol.Known(short)}
			return message
		}, nil},
		{"larger than the bound", func() protocol.Message {
			message := protocol.Message{}
			message.Activity = &protocol.Activity{Type: "tool", ImagePath: protocol.Known(oversized)}
			return message
		}, nil},
		{"inline index without links", func() protocol.Message { return protocol.Message{} }, new(int)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := serveImage(t, testCase.build(), testCase.index)
			assertUnavailable(t, err)
		})
	}
}

func TestNativeImageLinksKeepCommonMarkEncodedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "地形 image%.png")
	encoded := fileURL(t, path)
	raw, err := url.PathUnescape(encoded)
	if err != nil {
		t.Fatal(err)
	}
	links := NativeImageLinks("![raw](<" + raw + ">)\n![encoded](<" + encoded + ">)")
	if len(links) != 1 || links[0] != encoded {
		t.Fatalf("image identity changed: %q, want %q", links, encoded)
	}
	resolved, ok := LocalImagePath(links[0])
	if !ok || filepath.Clean(resolved) != filepath.Clean(path) {
		t.Fatal("normalized identity changed the authorized local file")
	}
}

func TestNativeImageLinksResolveMarkdownEntitiesBeforeEncoding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a&b.png")
	encoded := fileURL(t, path)
	markdown := strings.ReplaceAll(encoded, "&", "&#38;")
	links := NativeImageLinks("![entity](<" + markdown + ">)")
	if len(links) != 1 || links[0] != encoded {
		t.Fatalf("unexpected entity link %q", links)
	}
}
