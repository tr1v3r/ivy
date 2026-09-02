package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tr1v3r/ivy/driver"
)

// load() processor-type switch: every known type must map to the right
// Processor implementation, unknown types yield a nil processor, and a
// Load failure is warned but does not abort the directive.
func TestLoad_ProcessorTypeSwitch(t *testing.T) {
	rules := `[
		{"path":"/all","Processors":[
			{"type":"json","data":{"path":"a","type":"create","value":1}},
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

	directives := load()
	if len(directives) != 3 {
		t.Fatalf("expect 3 directives, got %d", len(directives))
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
		if got, ok := procs[i].(driver.Processor); !ok || got == nil {
			t.Errorf("processor %d does not implement driver.Processor", i)
		} else if reflect.TypeOf(procs[i]) != reflect.TypeOf(want) {
			t.Errorf("processor %d type = %T, want %T", i, procs[i], want)
		}
	}

	// bad data for a known type: Load fails (warned) but op is still appended
	bad := directives[1].Processors()
	if len(bad) != 1 {
		t.Fatalf("expect 1 processor on /bad, got %d", len(bad))
	}
	if bad[0] == nil {
		t.Fatal("processor for known type should be constructed even when Load fails")
	}

	// unknown type: op stays nil but keeps its slot
	unknown := directives[2].Processors()
	if len(unknown) != 1 {
		t.Fatalf("expect 1 processor on /unknown, got %d", len(unknown))
	}
	if unknown[0] != nil {
		t.Errorf("unknown type should leave nil processor, got %T", unknown[0])
	}
}

// default filename branch: unset RULES_FILE falls back to defaultFilename.
func TestLoad_DefaultFilenameFallback(t *testing.T) {
	t.Setenv("RULES_FILE", "")
	directives := load()
	// the default file ships with the repo; loading it must not panic and
	// must return whatever it contains (possibly nil if cwd differs)
	for _, d := range directives {
		if d == nil {
			t.Fatal("directive is nil")
		}
	}
}
