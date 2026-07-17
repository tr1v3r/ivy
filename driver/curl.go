package driver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tr1v3r/pkg/fetch"
)

var _ Processor = (*CURLProcessor)(nil)

const (
	maxCURLHeaderDetailBytes = 4 * 1024
	maxCURLBodyDetailBytes   = 4 * 1024
	redactedValue            = "[REDACTED]"
)

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

// CURLProcessor
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

	// A is the author of the Processor
	A string `json:"author"`
	// C is the create time of the Processor
	C time.Time `json:"created_at"`
}

func (op *CURLProcessor) Type() string         { return "curl" }
func (op *CURLProcessor) Path() string         { return op.P }
func (op *CURLProcessor) Author() string       { return op.A }
func (op *CURLProcessor) CreatedAt() time.Time { return op.C }
func (op *CURLProcessor) Load(data []byte) error {
	if err := json.Unmarshal(data, op); err != nil {
		return fmt.Errorf("unmarshal fail: %w", err)
	}
	return nil
}
func (op *CURLProcessor) Save() []byte {
	data, _ := json.Marshal(op)
	return data
}
func (op *CURLProcessor) Process(rc *RealizeContext, _ []byte) ([]byte, error) {
	method := strings.ToUpper(strings.TrimSpace(op.Method))
	if method == "" {
		method = "GET"
	}

	startedAt := time.Now()
	statusCode, content, responseHeader, err := fetch.DoRequestWithOptions(method, op.URL,
		[]fetch.RequestOption{fetch.WithHeaders(op.Header)}, bytes.NewReader(op.Body))
	duration := time.Since(startedAt)
	if err != nil {
		return nil, &CURLProcessError{
			Method:          method,
			URL:             op.URL,
			RequestHeader:   cloneHTTPHeader(op.Header),
			RequestBodySize: len(op.Body),
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
