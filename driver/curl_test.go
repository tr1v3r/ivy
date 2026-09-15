package driver_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestCURLProcessorHonorsRealizeContextCancellation guards issue #51 (M1):
// Process must propagate the RealizeContext cancellation to the outbound
// request instead of discarding it and running to the fetch client's
// fallback timeout.
func TestCURLProcessorHonorsRealizeContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slow upstream: outlives the caller's cancellation window but stays
		// well below any fallback timeout, so the only way Process returns
		// early is honoring rc.Context.
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("slow"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel() // caller gives up while the upstream is still sleeping
	}()
	rc := &driver.RealizeContext{Context: ctx}
	op := &driver.CURLProcessor{URL: srv.URL}

	start := time.Now()
	content, err := op.Process(rc, nil)
	duration := time.Since(start)

	if err == nil {
		t.Fatalf("Process() error = nil (content=%q), want context.Canceled after rc.Context was canceled", content)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Process() error = %v, want errors.Is(err, context.Canceled)", err)
	}
	var processErr *driver.CURLProcessError
	if !errors.As(err, &processErr) {
		t.Fatalf("Process() error type = %T, want *driver.CURLProcessError with diagnostics", err)
	}
	if duration >= 250*time.Millisecond {
		t.Fatalf("Process() returned after %v, want cancellation noticed well before the 300ms upstream response (context not propagated)", duration)
	}
}

// TestCURLProcessorWithoutRealizeContextContext keeps the nil-context corner
// safe: a RealizeContext without an embedded Context (or no rc at all) must
// still perform a normal, uncanceled request.
func TestCURLProcessorWithoutRealizeContextContext(t *testing.T) {
	useHTTPClient(t, func(request *http.Request) (*http.Response, error) {
		if request.Context() == nil {
			t.Error("outbound request carries no context")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    request,
		}, nil
	})

	for name, rc := range map[string]*driver.RealizeContext{
		"nil rc":              nil,
		"rc without embedded": {},
	} {
		content, err := (&driver.CURLProcessor{URL: "https://example.test"}).Process(rc, nil)
		if err != nil {
			t.Fatalf("%s: Process() error = %v, want nil", name, err)
		}
		if got := string(content); got != "ok" {
			t.Errorf("%s: Process() content = %q, want %q", name, got, "ok")
		}
	}
}
