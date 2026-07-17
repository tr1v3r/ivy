package driver_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tr1v3r/ivy/driver"
	"github.com/tr1v3r/pkg/fetch"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func useHTTPClient(t *testing.T, transport roundTripperFunc) {
	t.Helper()
	previous := fetch.DefaultClient()
	fetch.SetDefaultClient(&http.Client{Transport: transport})
	t.Cleanup(func() {
		fetch.SetDefaultClient(previous)
	})
}

func TestCURLProcessorReportsHTTPFailureDetails(t *testing.T) {
	const (
		responseBody = `{"error":"invalid payload","field":"name"}`
		secretToken  = "secret-bearer-token"
		sessionID    = "secret-session-id"
	)
	useHTTPClient(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnprocessableEntity,
			Header: http.Header{
				"Content-Type": {"application/json"},
				"Set-Cookie":   {"session=" + sessionID},
				"X-Request-Id": {"req-123"},
			},
			Body:    io.NopCloser(strings.NewReader(responseBody)),
			Request: request,
		}, nil
	})

	op := &driver.CURLProcessor{
		Method: "post",
		URL:    "https://example.test/users?access_token=query-secret&view=summary",
		Body:   []byte(`{"name":""}`),
		Header: map[string][]string{
			"Authorization": {"Bearer " + secretToken},
			"Content-Type":  {"application/json"},
		},
	}
	content, err := op.Process(nil, nil)
	if err == nil {
		t.Fatal("Process() error = nil, want an HTTP status error")
	}
	if content != nil {
		t.Fatalf("Process() content = %q, want nil", content)
	}

	var processErr *driver.CURLProcessError
	if !errors.As(err, &processErr) {
		t.Fatalf("Process() error type = %T, want *driver.CURLProcessError", err)
	}
	if processErr.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("StatusCode = %d, want %d", processErr.StatusCode, http.StatusUnprocessableEntity)
	}
	if got := string(processErr.ResponseBody); got != responseBody {
		t.Errorf("ResponseBody = %q, want %q", got, responseBody)
	}
	if got := processErr.ResponseHeader.Get("X-Request-ID"); got != "req-123" {
		t.Errorf("X-Request-ID = %q, want %q", got, "req-123")
	}

	message := err.Error()
	for _, want := range []string{
		"method=POST",
		"view=summary",
		"access_token=%5BREDACTED%5D",
		`"Authorization":["[REDACTED]"]`,
		"request_body_bytes=11",
		`status="422 Unprocessable Entity"`,
		`"X-Request-Id":["req-123"]`,
		`response_body="{\"error\":\"invalid payload\",\"field\":\"name\"}"`,
	} {
		if !strings.Contains(message, want) {
			t.Errorf("error message does not contain %q:\n%s", want, message)
		}
	}
	for _, secret := range []string{secretToken, sessionID, "query-secret"} {
		if strings.Contains(message, secret) {
			t.Errorf("error message leaked %q:\n%s", secret, message)
		}
	}
}

func TestCURLProcessorReportsRequestConstructionFailure(t *testing.T) {
	op := &driver.CURLProcessor{
		Method: " get ",
		URL:    "://invalid-url",
		Body:   []byte("request body"),
	}
	content, err := op.Process(nil, nil)
	if err == nil {
		t.Fatal("Process() error = nil, want a request construction error")
	}
	if content != nil {
		t.Fatalf("Process() content = %q, want nil", content)
	}

	var processErr *driver.CURLProcessError
	if !errors.As(err, &processErr) {
		t.Fatalf("Process() error type = %T, want *driver.CURLProcessError", err)
	}
	if processErr.Err == nil {
		t.Fatal("CURLProcessError.Err = nil, want underlying error")
	}
	if processErr.StatusCode != 0 {
		t.Errorf("StatusCode = %d, want 0", processErr.StatusCode)
	}
	for _, want := range []string{
		"method=GET",
		`url="://invalid-url"`,
		"request_body_bytes=12",
		`cause="build new request fail:`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message does not contain %q:\n%s", want, err)
		}
	}
}

func TestCURLProcessorRedactsURLInTransportFailure(t *testing.T) {
	useHTTPClient(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})

	const secret = "query-secret"
	op := &driver.CURLProcessor{
		URL: "https://example.test/users?access_token=" + secret,
	}
	_, err := op.Process(nil, nil)
	if err == nil {
		t.Fatal("Process() error = nil, want a transport error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error message leaked URL secret:\n%s", err)
	}
	if !strings.Contains(err.Error(), "access_token=%5BREDACTED%5D") {
		t.Fatalf("error message does not contain redacted URL:\n%s", err)
	}
}

func TestCURLProcessorKeepsSuccessfulResponse(t *testing.T) {
	useHTTPClient(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    request,
		}, nil
	})

	content, err := (&driver.CURLProcessor{URL: "https://example.test"}).Process(nil, nil)
	if err != nil {
		t.Fatalf("Process() error = %v, want nil", err)
	}
	if got := string(content); got != "ok" {
		t.Errorf("Process() content = %q, want %q", got, "ok")
	}
}
