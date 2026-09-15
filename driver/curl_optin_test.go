package driver_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tr1v3r/ivy/driver"
)

// Opt-in paths for the curl security knobs (issue #52, M2): per-rule
// insecure TLS for self-signed upstreams, per-rule max_bytes overrides, and
// config round-tripping of the new fields.

func TestCURLProcessorInsecureOptInAcceptsSelfSignedTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tls-ok"))
	}))
	defer srv.Close()

	content, err := (&driver.CURLProcessor{URL: srv.URL, Insecure: true}).Process(nil, nil)
	if err != nil {
		t.Fatalf("Process() error = %v, want nil for explicit insecure opt-in", err)
	}
	if got := string(content); got != "tls-ok" {
		t.Errorf("Process() content = %q, want %q", got, "tls-ok")
	}
}

func TestCURLProcessorEnforcesConfiguredMaxBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("b"), 1024))
	}))
	defer srv.Close()

	// configured cap below the body size: explicit error
	_, err := (&driver.CURLProcessor{URL: srv.URL, MaxBytes: 64}).Process(nil, nil)
	if err == nil {
		t.Fatal("Process() error = nil, want limit-exceeded error for max_bytes=64 vs 1KiB body")
	}
	if !strings.Contains(err.Error(), "max_bytes") {
		t.Errorf("error should name the max_bytes limit, got: %s", err)
	}

	// body below the configured cap passes through untouched
	content, err := (&driver.CURLProcessor{URL: srv.URL, MaxBytes: 2048}).Process(nil, nil)
	if err != nil {
		t.Fatalf("Process() error = %v, want nil under the cap", err)
	}
	if len(content) != 1024 {
		t.Errorf("Process() content length = %d, want the full 1024 bytes", len(content))
	}

	// negative cap explicitly disables the limit
	content, err = (&driver.CURLProcessor{URL: srv.URL, MaxBytes: -1}).Process(nil, nil)
	if err != nil {
		t.Fatalf("Process() error = %v, want nil for max_bytes=-1 (unlimited)", err)
	}
	if len(content) != 1024 {
		t.Errorf("Process() content length = %d, want the full 1024 bytes", len(content))
	}
}

func TestCURLProcessorSecurityConfigRoundTrip(t *testing.T) {
	op := &driver.CURLProcessor{URL: "https://example.test", Insecure: true, MaxBytes: 4096}
	var restored driver.CURLProcessor
	if err := restored.Load(op.Save()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !restored.Insecure || restored.MaxBytes != 4096 {
		t.Errorf("round trip lost security config: insecure=%v max_bytes=%d", restored.Insecure, restored.MaxBytes)
	}
}
