package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tr1v3r/ivy"
	"github.com/tr1v3r/ivy/driver"
)

// TestRefreshOnDemandOnly pins the refresh contract that cmd/serve relies
// on after the periodic 5s rebuild was removed (audit W3): a standard-mode
// tree realizes its processors exactly once at build time, plain reads are
// served from the built tree, and re-running processors happens only on an
// explicit RefreshForest call. A curl processor wired at "/" must not be
// replayed by anything other than an explicit refresh.
func TestRefreshOnDemandOnly(t *testing.T) {
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"up":true}`))
	}))
	defer upstream.Close()

	InitForest(DefaultBuilder(ivy.NewDirective("/", &driver.CURLProcessor{URL: upstream.URL})))

	if n := hits.Load(); n != 1 {
		t.Fatalf("startup build should realize the curl processor exactly once, got %d upstream hits", n)
	}

	// repeated reads must be served from the built tree, not re-realized
	r := newTestRouter()
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: expect 200, got %d (body: %s)", i, w.Code, w.Body.String())
		}
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("reads must not re-realize the standard-mode tree, got %d upstream hits", n)
	}

	// an explicit refresh re-runs the builder — the on-demand rebuild path
	RefreshForest()
	if n := hits.Load(); n != 2 {
		t.Errorf("explicit RefreshForest should re-realize exactly once, got %d upstream hits", n)
	}
}

// TestDefaultBuilderSurvivesUpstreamFailure guards the W4 crash: when a
// builder's realization fails (curl processor getting a 5xx from upstream),
// DefaultBuilder used to panic, and since NewForest stores builders without
// a recover wrapper the panic escaped forest.Build() — killing the process
// when it fired inside a background refresh goroutine (cmd/serve's ticker)
// or crashing startup with a raw panic. The builder must log, return nil
// (forest.Build skips nil trees), and leave the previously built tree
// serving.
func TestDefaultBuilderSurvivesUpstreamFailure(t *testing.T) {
	var hits atomic.Int64
	var fail atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"up":true}`))
	}))
	defer upstream.Close()

	InitForest(DefaultBuilder(ivy.NewDirective("/", &driver.CURLProcessor{URL: upstream.URL})))
	if n := hits.Load(); n != 1 {
		t.Fatalf("startup build should realize the curl processor once, got %d upstream hits", n)
	}

	r := newTestRouter()
	get := func() int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
		if w.Code != http.StatusOK {
			return w.Code
		}
		// response is the rule bytes wrapped as a JSON base64 string
		var payload any
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatalf("invalid json response: %s", w.Body.String())
		}
		encoded, _ := payload.(string)
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("response is not base64 rule bytes: %s", w.Body.String())
		}
		if !strings.Contains(string(decoded), `"up":true`) {
			t.Errorf("unexpected rule content: %s", decoded)
		}
		return w.Code
	}
	if code := get(); code != http.StatusOK {
		t.Fatalf("healthy tree should serve 200, got %d", code)
	}

	// upstream starts failing: the refresh build fails, but neither the
	// refresh nor any request may take the process down, and the
	// previously built tree keeps serving.
	fail.Store(true)
	RefreshForest() // panicked on the old code

	if code := get(); code != http.StatusOK {
		t.Errorf("previous tree must keep serving after failed refresh, got %d", code)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("failed refresh should have attempted the upstream exactly once more, got %d hits", n)
	}
}

// TestDefaultCacheBuilder_TTLRefresh pins the RULES_TTL contract (audit
// follow-up to W3): a cache-TTL rule tree realizes lazily on first access,
// serves from cache within the TTL window, and re-runs its processors (curl
// re-fetches the rule URL) on the first access after expiry. No timer, no
// replay without traffic — and the re-realized root must be fresh content,
// not compounded on the previous output (guards the F1 fix under TTL).
func TestDefaultCacheBuilder_TTLRefresh(t *testing.T) {
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"up":true}`))
	}))
	defer upstream.Close()

	const ttl = 50 * time.Millisecond
	InitForest(DefaultCacheBuilder(ttl, ivy.NewDirective("/", &driver.CURLProcessor{URL: upstream.URL})))

	// lazy: nothing realized before the first access
	if n := hits.Load(); n != 0 {
		t.Fatalf("lazy tree must not realize before first access, got %d upstream hits", n)
	}

	r := newTestRouter()
	get := func() {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("expect 200, got %d (body: %s)", w.Code, w.Body.String())
		}
		if body := w.Body.String(); strings.Contains(body, `{"up":true}{"up":true}`) {
			t.Fatalf("compounded output detected, got %s", body)
		}
	}

	get()
	if n := hits.Load(); n != 1 {
		t.Fatalf("first access should realize once, got %d upstream hits", n)
	}

	get()
	if n := hits.Load(); n != 1 {
		t.Errorf("within TTL must serve from cache, got %d upstream hits", n)
	}

	time.Sleep(ttl + 30*time.Millisecond)
	get()
	if n := hits.Load(); n != 2 {
		t.Errorf("after TTL the next access must re-realize, got %d upstream hits", n)
	}
}
