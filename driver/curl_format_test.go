package driver_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tr1v3r/ivy/driver"
)

// Coverage for CURLProcessError message formatting branches that the main
// curl suite never hits: empty body, non-UTF-8 body, truncated long details,
// and password-style userinfo redaction. All HTTP goes through the injected
// round-tripper — no real network.

func TestCURLProcessorErrorFormatsEmptyResponseBody(t *testing.T) {
	useHTTPClient(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	})

	_, err := (&driver.CURLProcessor{URL: "https://example.test"}).Process(nil, nil)
	if err == nil {
		t.Fatal("Process() error = nil, want status error")
	}
	if !strings.Contains(err.Error(), `response_body=""`) {
		t.Errorf("empty body should format as quoted empty string:\n%s", err)
	}
}

func TestCURLProcessorErrorFormatsNonUTF8ResponseBody(t *testing.T) {
	useHTTPClient(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte{0xff, 0xfe, 0x00, 0x01})),
			Request:    request,
		}, nil
	})

	_, err := (&driver.CURLProcessor{URL: "https://example.test"}).Process(nil, nil)
	if err == nil {
		t.Fatal("Process() error = nil, want status error")
	}
	if !strings.Contains(err.Error(), "4 bytes of non-UTF-8 response data") {
		t.Errorf("non-UTF-8 body should be replaced by a size placeholder:\n%s", err)
	}
}

func TestCURLProcessorErrorTruncatesLongDetails(t *testing.T) {
	longBody := strings.Repeat("x", 8192)
	longHeaderValue := strings.Repeat("h", 8192)

	useHTTPClient(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"X-Long": {longHeaderValue}},
			Body:       io.NopCloser(strings.NewReader(longBody)),
			Request:    request,
		}, nil
	})

	_, err := (&driver.CURLProcessor{URL: "https://example.test"}).Process(nil, nil)
	if err == nil {
		t.Fatal("Process() error = nil, want status error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "<truncated; total_bytes=") {
		t.Errorf("long details should carry truncation marker:\n%.200s...", msg)
	}
	// the truncation marker must appear for both body and header details
	if got := strings.Count(msg, "<truncated; total_bytes="); got < 2 {
		t.Errorf("expected at least 2 truncation markers (header+body), got %d", got)
	}
}

func TestCURLProcessorRedactsURLUserPassword(t *testing.T) {
	useHTTPClient(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})

	const secret = "hunter2"
	op := &driver.CURLProcessor{URL: "https://user:" + secret + "@example.test/path"}
	_, err := op.Process(nil, nil)
	if err == nil {
		t.Fatal("Process() error = nil, want transport error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error message leaked URL password:\n%s", err)
	}
	// inside a URL string the brackets of [REDACTED] are percent-escaped
	if !strings.Contains(err.Error(), "%5B"+"REDACTED"+"%5D") {
		t.Fatalf("password should be replaced by percent-escaped REDACTED in URLs:\n%s", err)
	}
	if !strings.Contains(err.Error(), "user:") {
		t.Fatalf("username should be preserved in userinfo:\n%s", err)
	}
}
