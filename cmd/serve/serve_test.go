package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tr1v3r/ivy/driver"
)

func Test_Rule(t *testing.T) {
	var items = []RuleDataItem{
		{Path: "/", Processors: []struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}{
			{Type: "curl", Data: (&driver.CURLProcessor{URL: "https://r1v3.com/ping", C: time.Now()}).Save()},
		}},
	}

	data, err := json.Marshal(items)
	if err != nil {
		t.Errorf("marshal rules fail: %s", err)
		return
	}
	t.Logf("got rules data: %s", data)
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "rules.json")

	rules := `[{"path":"/","Processors":[` +
		`{"type":"curl","data":{"url":"http://localhost:1/ping","author":"","created_at":"0001-01-01T00:00:00Z"}},` +
		`{"type":"curl","data":{}},` +
		`{"type":"unknown","data":{}}` +
		`]}]`
	if err := os.WriteFile(file, []byte(rules), 0o600); err != nil {
		t.Fatalf("write rules fail: %s", err)
	}

	t.Setenv("RULES_FILE", file)
	directives := load()
	if len(directives) != 1 {
		t.Fatalf("expect 1 directive, got %d", len(directives))
	}
	if directives[0].Path() != "/" {
		t.Errorf("unexpected path: %s", directives[0].Path())
	}
	procs := directives[0].Processors()
	if len(procs) != 3 {
		t.Fatalf("expect 3 processors, got %d", len(procs))
	}
	if procs[0] == nil {
		t.Fatal("curl processor should be loaded")
	}
	if procs[0].Type() != "curl" {
		t.Errorf("unexpected processor type: %s", procs[0].Type())
	}
}

func TestLoad_DefaultFileMissing(t *testing.T) {
	// no RULES_FILE set: falls back to a path that does not exist for tests
	t.Setenv("RULES_FILE", "/nonexistent/rules.json")
	if directives := load(); directives != nil {
		t.Errorf("expected nil directives on missing file, got %d", len(directives))
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(file, []byte("not-json"), 0o600); err != nil {
		t.Fatalf("write rules fail: %s", err)
	}

	t.Setenv("RULES_FILE", file)
	if directives := load(); directives != nil {
		t.Errorf("expected nil directives on invalid json, got %d", len(directives))
	}
}

func TestRegister(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := register(gin.New())
	if r == nil {
		t.Fatal("expected gin engine from register")
	}

	// the api group and swagger route must be wired
	found := false
	for _, route := range r.Routes() {
		if route.Path == "/api/v1/rule" && route.Method == http.MethodGet {
			found = true
		}
	}
	if !found {
		t.Error("expected GET /api/v1/rule registered")
	}
}
