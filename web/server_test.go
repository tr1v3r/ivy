package web

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

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
