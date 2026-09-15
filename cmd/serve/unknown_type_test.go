package main

import (
	"os"
	"path/filepath"
	"testing"
)

// #60 (W7): the processor-type switch had no default branch. An
// unknown type (a typo like "Json", an empty type, or a genuinely new
// kind) left op nil, and load() still appended the nil slot —
// StdRealizer silently skips nil processors (driver/common.go), so the
// rule degraded to a no-op with no warning anywhere. These tests pin
// the new behavior: the offending op is dropped and an error naming
// the rule and op index is logged (visible in `go test -v` output).

func writeRulesJSON(t *testing.T, content string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("write rules fail: %s", err)
	}
	return file
}

// An unknown-type op must be dropped from the chain while its healthy
// sibling op keeps working. On unfixed master the nil slot was appended
// (procs length 2 with a nil tail) and the rule silently no-opped —
// this test fails there.
func TestLoad_UnknownTypeDropped_SiblingKept(t *testing.T) {
	rules := `[
		{"path":"/mixed","Processors":[
			{"type":"mystery","data":{}},
			{"type":"json","data":{"path":"g","type":"create","value":"MQ=="}}
		]}
	]`
	t.Setenv("RULES_FILE", writeRulesJSON(t, rules))

	directives, err := load()
	if err != nil {
		t.Fatalf("load fail: %s", err)
	}
	if len(directives) != 1 {
		t.Fatalf("expect 1 directive, got %d", len(directives))
	}
	procs := directives[0].Processors()
	if len(procs) != 1 {
		t.Fatalf("/mixed must keep only the json op, got %d processors — an unknown-type nil slot was appended", len(procs))
	}
	if procs[0] == nil {
		t.Fatal("surviving processor must be the loaded json op, got nil")
	}
	if procs[0].Path() != "g" || procs[0].Type() != "create" {
		t.Errorf("surviving processor identity: path=%q type=%q, want path=g type=create",
			procs[0].Path(), procs[0].Type())
	}
}

// The issue's exact trigger: a case typo "Json" is not the json
// processor — it must be dropped, not silently no-op'd.
func TestLoad_UnknownTypeCaseTypoDropped(t *testing.T) {
	rules := `[
		{"path":"/typo","Processors":[
			{"type":"Json","data":{"path":"g","type":"create","value":"MQ=="}}
		]}
	]`
	t.Setenv("RULES_FILE", writeRulesJSON(t, rules))

	directives, err := load()
	if err != nil {
		t.Fatalf("load fail: %s", err)
	}
	// the op is dropped (#60) and — composed with #58 — a directive left
	// with no surviving ops is dropped as well
	if len(directives) != 0 {
		t.Fatalf("typo-only directive must be dropped (op dropped + empty-ops rule), got %d", len(directives))
	}
}

// A directive whose only op has an unknown type ends with an empty
// chain; an empty type string is unknown too.
func TestLoad_UnknownTypeOnlyAndEmptyTypeDropped(t *testing.T) {
	rules := `[
		{"path":"/only-unknown","Processors":[
			{"type":"mystery","data":{}}
		]},
		{"path":"/empty-type","Processors":[
			{"type":"","data":{}}
		]}
	]`
	t.Setenv("RULES_FILE", writeRulesJSON(t, rules))

	directives, err := load()
	if err != nil {
		t.Fatalf("load fail: %s", err)
	}
	// both directives lost their only op to the unknown-type drop (#60);
	// composed with #58's empty-ops rule the directives go too, with a
	// loud log naming them
	if len(directives) != 0 {
		t.Fatalf("all-unknown directives must be dropped (composed #60+#58 semantics), got %d (%s)",
			len(directives), directivePaths(directives))
	}
}

// Known types are untouched: every documented type still constructs
// and enters the chain.
func TestLoad_KnownTypesStillLoad(t *testing.T) {
	rules := `[
		{"path":"/all","Processors":[
			{"type":"json","data":{"path":"a","type":"create","value":"MQ=="}},
			{"type":"yaml","data":{"path":"a","type":"create","yaml_path":"a"}},
			{"type":"curl","data":{"url":"http://localhost:1/ping"}},
			{"type":"xml","data":{"path":"a","type":"create","xml_path":"root/a"}},
			{"type":"toml","data":{"path":"a","type":"create","toml_path":"a"}},
			{"type":"template","data":{"path":"a"}}
		]}
	]`
	t.Setenv("RULES_FILE", writeRulesJSON(t, rules))

	directives, err := load()
	if err != nil {
		t.Fatalf("load fail: %s", err)
	}
	if len(directives) != 1 {
		t.Fatalf("expect 1 directive, got %d", len(directives))
	}
	procs := directives[0].Processors()
	if len(procs) != 6 {
		t.Fatalf("all 6 known types must load, got %d", len(procs))
	}
	for i, p := range procs {
		if p == nil {
			t.Errorf("processor %d must be non-nil", i)
		}
	}
}
