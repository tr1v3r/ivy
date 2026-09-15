package driver

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var _ Processor = (*CURLProcessor)(nil)

// Diagnostic-detail limits and the placeholder shown for redacted values
// in CURLProcessError messages.
const (
	maxCURLHeaderDetailBytes = 4 * 1024
	maxCURLBodyDetailBytes   = 4 * 1024
	redactedValue            = "[REDACTED]"
)

// curl security posture (issue #52, M2).
const (
	// defaultCURLMaxResponseBodyBytes caps response bodies when a processor
	// does not set MaxBytes, so a malicious upstream cannot exhaust memory
	// with an unbounded response.
	defaultCURLMaxResponseBodyBytes = 10 << 20 // 10 MiB
	// curlAllowHostsEnv names the process-level upstream hostname
	// allowlist: a comma-separated list of hostnames. Unset or empty allows
	// every host (out-of-box default); otherwise a curl URL — and every
	// redirect target — must match one entry exactly (case-insensitive,
	// ports are ignored).
	curlAllowHostsEnv = "IVY_CURL_ALLOW_HOSTS"
	// curlRequestTimeout is the fallback request timeout, preserving the
	// historical fetch-client behavior for callers that provide no
	// cancellation signal of their own.
	curlRequestTimeout = 60 * time.Second
	// curlMaxRedirects mirrors net/http's default redirect limit.
	curlMaxRedirects = 10
)

// ivy owns the curl HTTP clients instead of reusing fetch.DefaultClient:
// the TLS posture of every rule must not hinge on a process-global mutable
// default — any library calling fetch.SetDefaultClient would otherwise
// decide (or silently disable) certificate verification for ivy rules.
var (
	curlVerifyingClient = newCURLClient(&tls.Config{MinVersion: tls.VersionTLS12})
	curlInsecureClient  = newCURLClient(&tls.Config{InsecureSkipVerify: true}) //nolint:gosec // explicit per-rule opt-in via "insecure": true
)

// newCURLClient builds one of ivy's curl clients over a cloned default
// transport (standard pooling, ProxyFromEnvironment). Redirect targets are
// revalidated against the same host allowlist as the initial URL, so a 302
// from an allowed host to an internal address cannot bypass it.
func newCURLClient(tlsConfig *tls.Config) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &http.Client{
		Timeout:   curlRequestTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= curlMaxRedirects {
				return fmt.Errorf("curl: stopped after %d redirects", curlMaxRedirects)
			}
			if !curlHostAllowed(req.URL.Hostname()) {
				return fmt.Errorf("curl: redirect to host %q blocked by the %s allowlist", req.URL.Hostname(), curlAllowHostsEnv)
			}
			return nil
		},
	}
}

// CURLProcessError contains the request and response details needed to diagnose
// a failed CURLProcessor request. Sensitive header and URL values are redacted
// from Error(), while the structured fields retain the original values for
// callers that explicitly inspect the error.
type CURLProcessError struct {
	Method          string
	URL             string
	RequestHeader   http.Header
	RequestBodySize int
	StatusCode      int
	ResponseHeader  http.Header
	ResponseBody    []byte
	Duration        time.Duration
	Err             error
}

func (e *CURLProcessError) Error() string {
	var details = []string{
		fmt.Sprintf("method=%s", e.Method),
		fmt.Sprintf("url=%s", strconv.Quote(redactURL(e.URL))),
		fmt.Sprintf("request_headers=%s", formatCURLHeaders(e.RequestHeader)),
		fmt.Sprintf("request_body_bytes=%d", e.RequestBodySize),
		fmt.Sprintf("duration=%s", e.Duration),
	}
	if e.StatusCode != 0 {
		status := fmt.Sprintf("%d", e.StatusCode)
		if text := http.StatusText(e.StatusCode); text != "" {
			status += " " + text
		}
		details = append(details,
			fmt.Sprintf("status=%s", strconv.Quote(status)),
			fmt.Sprintf("response_headers=%s", formatCURLHeaders(e.ResponseHeader)),
			fmt.Sprintf("response_body=%s", formatCURLBody(e.ResponseBody)),
		)
	}
	if e.Err != nil {
		details = append(details, fmt.Sprintf("cause=%s", strconv.Quote(formatCURLCause(e.Err, e.URL))))
	}
	return "curl request failed: " + strings.Join(details, " ")
}

// Unwrap exposes transport, request-construction, and response-body read
// failures to errors.Is/errors.As.
func (e *CURLProcessError) Unwrap() error { return e.Err }

// CURLProcessor fetches remote content over HTTP and returns it as the node content.
type CURLProcessor struct {
	// P is the target path of the Processor
	P string `json:"path,omitempty"`

	// URL the target url
	URL string `json:"url"`
	// Method the method to call URL
	Method string `json:"method,omitempty"`
	// Body post with data
	Body   []byte              `json:"body,omitempty"`
	Header map[string][]string `json:"header,omitempty"`

	// Insecure skips TLS certificate verification for this processor's
	// upstream, e.g. self-signed certificates. Default false: certificates
	// are verified (MITM protection). Opt in per rule, for trusted networks
	// only.
	Insecure bool `json:"insecure,omitempty"`

	// MaxBytes caps the accepted response body size in bytes. 0 applies the
	// package default (10 MiB); a positive value sets the cap; a negative
	// value disables it. Oversized bodies yield a *CURLProcessError naming
	// max_bytes instead of exhausting process memory.
	MaxBytes int64 `json:"max_bytes,omitempty"`

	// A is the author of the Processor
	A string `json:"author"`
	// C is the create time of the Processor
	C time.Time `json:"created_at"`
}

// Type returns "curl".
func (op *CURLProcessor) Type() string { return "curl" }

// Path returns the target tree path of the Processor.
func (op *CURLProcessor) Path() string { return op.P }

// Author returns the processor author.
func (op *CURLProcessor) Author() string { return op.A }

// CreatedAt returns the processor creation time.
func (op *CURLProcessor) CreatedAt() time.Time { return op.C }

// Load populates the processor from its JSON serialization.
func (op *CURLProcessor) Load(data []byte) error {
	if err := json.Unmarshal(data, op); err != nil {
		return fmt.Errorf("unmarshal fail: %w", err)
	}
	return nil
}

// Save returns the JSON serialization of the processor.
func (op *CURLProcessor) Save() []byte {
	data, _ := json.Marshal(op)
	return data
}

// Process performs the HTTP request; non-2xx responses yield a *CURLProcessError with full diagnostics.
//
// The context carried by rc (when present) is propagated to the outbound
// request: a canceled RealizeContext aborts the upstream call promptly with
// a *CURLProcessError wrapping context.Canceled, instead of occupying the
// upstream connection until the fetch client's fallback timeout. A missing
// rc or embedded context falls back to context.Background().
//
// Security posture: TLS certificates are verified unless the processor opts
// in via Insecure; response bodies above MaxBytes (default 10 MiB) fail with
// an explicit error; and both the URL and any redirect target must pass the
// IVY_CURL_ALLOW_HOSTS allowlist when it is set (empty allows all hosts),
// with the URL scheme restricted to http/https.
func (op *CURLProcessor) Process(rc *RealizeContext, _ []byte) ([]byte, error) {
	method := strings.ToUpper(strings.TrimSpace(op.Method))
	if method == "" {
		method = "GET"
	}

	ctx := context.Background()
	if rc != nil && rc.Context != nil {
		ctx = rc.Context
	}

	startedAt := time.Now()
	// The caller's context travels on the native request so cancellation
	// aborts the upstream call while preserving the 4-value diagnostics
	// (the fetch helper that drops response headers is no longer used).
	statusCode, content, responseHeader, err := op.doRequest(ctx, method)
	duration := time.Since(startedAt)
	if err != nil {
		return nil, &CURLProcessError{
			Method:          method,
			URL:             op.URL,
			RequestHeader:   cloneHTTPHeader(op.Header),
			RequestBodySize: len(op.Body),
			StatusCode:      statusCode,
			ResponseHeader:  responseHeader.Clone(),
			Duration:        duration,
			Err:             err,
		}
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return nil, &CURLProcessError{
			Method:          method,
			URL:             op.URL,
			RequestHeader:   cloneHTTPHeader(op.Header),
			RequestBodySize: len(op.Body),
			StatusCode:      statusCode,
			ResponseHeader:  responseHeader.Clone(),
			ResponseBody:    bytes.Clone(content),
			Duration:        duration,
		}
	}
	return content, nil
}

// doRequest performs the outbound request with ivy's own clients and
// applies the URL scheme/allowlist pre-checks and the response body cap.
// ctx is threaded into the request so caller cancellation aborts the
// upstream call promptly; the clients' own timeout remains the fallback
// for contexts without a deadline.
func (op *CURLProcessor) doRequest(ctx context.Context, method string) (statusCode int, content []byte, responseHeader http.Header, err error) {
	if err := op.checkURL(); err != nil {
		return 0, nil, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, method, op.URL, bytes.NewReader(op.Body))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("build new request fail: %w", err)
	}
	for key, values := range op.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	client := curlVerifyingClient
	if op.Insecure {
		client = curlInsecureClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return -1, nil, nil, err
	}
	defer resp.Body.Close() //nolint:errcheck

	limit := op.maxResponseBodyBytes()
	var reader io.Reader = resp.Body
	if limit >= 0 && limit < math.MaxInt64 {
		// read one byte past the cap so overflow is detectable
		reader = io.LimitReader(resp.Body, limit+1)
	}
	content, err = io.ReadAll(reader)
	if err != nil {
		return -1, nil, nil, err
	}
	if limit >= 0 && int64(len(content)) > limit {
		return resp.StatusCode, nil, resp.Header, fmt.Errorf(
			"response body exceeds the curl max_bytes limit: got at least %d bytes, limit is %d (raise max_bytes on the curl processor, or set it to -1 to disable the cap)",
			int64(len(content)), limit)
	}
	return resp.StatusCode, content, resp.Header, nil
}

// checkURL enforces the curl URL scheme and host allowlist before any
// connection is attempted.
func (op *CURLProcessor) checkURL() error {
	parsed, err := url.Parse(op.URL)
	if err != nil {
		return fmt.Errorf("build new request fail: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("curl url scheme %q is not allowed, want http or https", parsed.Scheme)
	}
	if !curlHostAllowed(parsed.Hostname()) {
		return fmt.Errorf("curl upstream host %q is not allowed by the %s allowlist", parsed.Hostname(), curlAllowHostsEnv)
	}
	return nil
}

// maxResponseBodyBytes resolves the effective response body cap: a positive
// MaxBytes sets it, zero selects the package default, negative disables it.
func (op *CURLProcessor) maxResponseBodyBytes() int64 {
	switch {
	case op.MaxBytes < 0:
		return -1
	case op.MaxBytes == 0:
		return defaultCURLMaxResponseBodyBytes
	default:
		return op.MaxBytes
	}
}

// curlHostAllowed reports whether host passes the IVY_CURL_ALLOW_HOSTS
// allowlist. An unset or empty allowlist allows every host; otherwise the
// hostname must equal one comma-separated entry (case-insensitive, ports
// ignored).
func curlHostAllowed(host string) bool {
	allowlist := os.Getenv(curlAllowHostsEnv)
	if strings.TrimSpace(allowlist) == "" {
		return true
	}
	host = strings.ToLower(strings.TrimSpace(host))
	for _, entry := range strings.Split(allowlist, ",") {
		if strings.ToLower(strings.TrimSpace(entry)) == host {
			return true
		}
	}
	return false
}

func cloneHTTPHeader(header map[string][]string) http.Header {
	if header == nil {
		return nil
	}
	return http.Header(header).Clone()
}

func formatCURLHeaders(header http.Header) string {
	if len(header) == 0 {
		return "{}"
	}
	safe := header.Clone()
	for name := range safe {
		if isSensitiveHTTPName(name) {
			safe[name] = []string{redactedValue}
		}
	}
	data, err := json.Marshal(safe)
	if err != nil {
		return strconv.Quote(fmt.Sprintf("<unavailable: %v>", err))
	}
	return truncateCURLDetail(string(data), maxCURLHeaderDetailBytes)
}

func formatCURLBody(body []byte) string {
	if len(body) == 0 {
		return strconv.Quote("")
	}
	if !utf8.Valid(body) {
		return strconv.Quote(fmt.Sprintf("<%d bytes of non-UTF-8 response data>", len(body)))
	}
	return strconv.Quote(truncateCURLDetail(string(body), maxCURLBodyDetailBytes))
}

func formatCURLCause(err error, rawURL string) string {
	formatted := err.Error()
	var urlError *url.Error
	if errors.As(err, &urlError) {
		formatted = strings.ReplaceAll(formatted, urlError.URL, redactURL(urlError.URL))
	}
	return strings.ReplaceAll(formatted, rawURL, redactURL(rawURL))
}

func truncateCURLDetail(detail string, limit int) string {
	if len(detail) <= limit {
		return detail
	}
	return detail[:limit] + fmt.Sprintf("... <truncated; total_bytes=%d>", len(detail))
}

func redactURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if parsed.User != nil {
		if _, hasPassword := parsed.User.Password(); hasPassword {
			parsed.User = url.UserPassword(parsed.User.Username(), redactedValue)
		}
	}
	query := parsed.Query()
	for name := range query {
		if isSensitiveHTTPName(name) {
			query[name] = []string{redactedValue}
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func isSensitiveHTTPName(name string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(name, "_", "-"))
	switch normalized {
	case "authorization", "proxy-authorization", "cookie", "set-cookie":
		return true
	}
	return strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "password") ||
		strings.Contains(normalized, "api-key") ||
		strings.Contains(normalized, "apikey") ||
		strings.Contains(normalized, "signature") ||
		strings.Contains(normalized, "credential")
}
