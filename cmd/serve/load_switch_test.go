package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tr1v3r/ivy"
	"github.com/tr1v3r/ivy/driver"
)

// load() processor-type switch: every known type must map to the right
// Processor implementation, unknown types yield a nil processor slot
// (W7/#60 scope), and a Load failure drops the op (#58/W5) — a
// half-unmarshaled zero-value processor never enters the chain.
func TestLoad_ProcessorTypeSwitch(t *testing.T) {
	rules := `[
		{"path":"/all","Processors":[
			{"type":"json","data":{"path":"a","type":"create","value":"MQ=="}},
			{"type":"yaml","data":{"path":"a","type":"create","yaml_path":"a"}},
			{"type":"curl","data":{"url":"http://localhost:1/ping"}},
			{"type":"xml","data":{"path":"a","type":"create","xml_path":"root/a"}},
			{"type":"toml","data":{"path":"a","type":"create","toml_path":"a"}},
			{"type":"template","data":{"path":"a"}}
		]},
		{"path":"/bad","Processors":[
			{"type":"json","data":42}
		]},
		{"path":"/unknown","Processors":[
			{"type":"mystery","data":{}}
		]}
	]`
	file := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(file, []byte(rules), 0o600); err != nil {
		t.Fatalf("write rules fail: %s", err)
	}
	t.Setenv("RULES_FILE", file)

	directives, err := load()
	if err != nil {
		t.Fatalf("load fail: %s", err)
	}
	// /bad is dropped: its only op failed to Load (#58/W5)
	if len(directives) != 2 {
		t.Fatalf("expect 2 directives (/bad dropped), got %d", len(directives))
	}

	// the per-format processors' Type() returns their operation type
	// ("create", ...), so pin the concrete implementation instead
	wantImpls := []driver.Processor{
		&driver.JSONProcessor{}, &driver.YAMLProcessor{}, &driver.CURLProcessor{},
		&driver.XMLProcessor{}, &driver.TOMLProcessor{}, &driver.TemplateProcessor{},
	}
	procs := directives[0].Processors()
	if len(procs) != len(wantImpls) {
		t.Fatalf("expect %d processors, got %d", len(wantImpls), len(procs))
	}
	for i, want := range wantImpls {
		if procs[i] == nil {
			t.Errorf("processor %d (%T) is nil, want loaded", i, want)
			continue
		}
		// procs[i] is already typed driver.Processor, so a type assertion to
		// the same interface is always true (staticcheck S1040); compare the
		// concrete types the way the assertion's branch did before.
		if reflect.TypeOf(procs[i]) != reflect.TypeOf(want) {
			t.Errorf("processor %d type = %T, want %T", i, procs[i], want)
		}
	}

	// /bad: the only op failed to Load (data 42 does not unmarshal into
	// a processor), so the op is dropped and the directive with it — a
	// zero-value processor must never enter a chain (#58/W5)
	if directives[1].Path() != "/unknown" {
		t.Errorf("expected /bad to be dropped, directives are %s", directivePaths(directives))
	}

	// unknown type: op stays nil but keeps its slot
	unknown := directives[1].Processors()
	if len(unknown) != 1 {
		t.Fatalf("expect 1 processor on /unknown, got %d", len(unknown))
	}
	if unknown[0] != nil {
		t.Errorf("unknown type should leave nil processor, got %T", unknown[0])
	}
}

func directivePaths(directives []ivy.Directive) string {
	paths := ""
	for i, d := range directives {
		if i > 0 {
			paths += ", "
		}
		paths += d.Path()
	}
	return "[" + paths + "]"
}

// default filename branch: unset RULES_FILE falls back to defaultFilename.
func TestLoad_DefaultFilenameFallback(t *testing.T) {
	t.Setenv("RULES_FILE", "")
	// go test runs with CWD=cmd/serve, so the default resolves to the
	// repo's shipped conf/rules.json (from any other CWD load now
	// returns an explicit error instead of a silent empty tree)
	directives, err := load()
	if err != nil {
		t.Fatalf("default rules file must load, got: %s", err)
	}
	if len(directives) == 0 {
		t.Fatal("shipped conf/rules.json must yield directives")
	}
	for _, d := range directives {
		if d == nil {
			t.Fatal("directive is nil")
		}
	}
}
