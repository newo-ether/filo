package service

import (
	"context"
	"flag"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/newo-ether/conch/encryptedhttp"
)

// RunRequest is the installer's encrypted control client. Credentials stay in a
// private file, never command arguments or logs; an uncertain mutation is not retried.
func RunRequest(arguments []string, output, diagnostic io.Writer) int {
	if output == nil {
		output = os.Stdout
	}
	if diagnostic == nil {
		diagnostic = os.Stderr
	}
	flags := flag.NewFlagSet("request", flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	address := flags.String("url", "", "Filo base URL")
	tokenFile := flags.String("token-file", "", "Private credential file")
	method := flags.String("method", "GET", "HTTP method")
	path := flags.String("path", "/v1/info", "Application path")
	bodyFile := flags.String("body-file", "", "Optional raw request file")
	timeout := flags.Duration("timeout", 30*time.Second, "Request deadline")
	if flags.Parse(arguments) != nil {
		return 2
	}
	base, err := url.Parse(*address)
	if err != nil || base.Host == "" || base.User != nil ||
		(base.Scheme != "http" && base.Scheme != "https") ||
		(base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.Fragment != "" ||
		!strings.HasPrefix(*path, "/v1/") || *timeout <= 0 || *timeout > 5*time.Minute {
		writeEntryDiagnostic(diagnostic, "Filo request failed: ", "invalid endpoint or deadline")
		return 2
	}
	token, err := readEntryText(*tokenFile)
	if err != nil || !tokenPattern.MatchString(token) {
		writeEntryDiagnostic(diagnostic, "Filo request failed: ", "credential file is unavailable")
		return 2
	}
	var body io.Reader
	if *bodyFile != "" {
		file, err := os.Open(*bodyFile)
		if err != nil {
			writeEntryDiagnostic(diagnostic, "Filo request failed: ", "request file is unavailable")
			return 2
		}
		defer file.Close()
		body = file
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, *method, strings.TrimSuffix(*address, "/")+*path, body)
	if err != nil {
		writeEntryDiagnostic(diagnostic, "Filo request failed: ", "invalid request")
		return 2
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &encryptedhttp.Transport{Key: []byte(token)}}
	response, err := client.Do(request)
	if err != nil {
		writeEntryDiagnostic(diagnostic, "Filo request failed: ", err.Error())
		return 1
	}
	defer response.Body.Close()
	raw, err := encryptedhttp.ReadAllBounded(response.Body, 1<<20)
	if err != nil {
		writeEntryDiagnostic(diagnostic, "Filo request failed: ", err.Error())
		return 1
	}
	if _, err := output.Write(raw); err != nil {
		return 1
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return 1
	}
	return 0
}
