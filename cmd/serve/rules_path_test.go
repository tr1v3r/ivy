package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRules writes a conf/rules.json with exactly one directive at the
// marker path under dir, mimicking the repo layout.
func writeRules(t *testing.T, dir, marker string) string {
	t.Helper()
	confDir := filepath.Join(dir, "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatalf("mkdir conf fail: %s", err)
	}
	rules := `[{"path":"` + marker + `","Processors":[` +
		`{"type":"json","data":{"path":"a","type":"create","value":"MQ=="}}]}]`
	file := filepath.Join(confDir, "rules.json")
	if err := os.WriteFile(file, []byte(rules), 0o600); err != nil {
		t.Fatalf("write rules fail: %s", err)
	}
	return file
}

// #59 (W6): the default rules path must resolve when the server is
// started from the repo root, the layout every documented command uses
// (AGENTS.md: `go run ./cmd/serve`). The legacy single default
// "../../conf/rules.json" only hit when CWD happened to be cmd/serve;
// from the repo root it escaped the repo and load() silently returned
// nil — an empty forest with the server still serving.
func TestLoad_DefaultRulesFromRepoRoot(t *testing.T) {
	root := t.TempDir()
	writeRules(t, root, "/w6-repo-root")

	t.Setenv("RULES_FILE", "")
	t.Chdir(root)

	directives, err := load()
	if err != nil {
		t.Fatalf("load fail: %s", err)
	}
	if len(directives) != 1 {
		t.Fatalf("expect 1 directive from default conf/rules.json relative to repo root, got %d", len(directives))
	}
	if directives[0].Path() != "/w6-repo-root" {
		t.Errorf("unexpected path: %s", directives[0].Path())
	}
}

// The legacy launch layout (CWD two levels below the conf dir, e.g.
// `go test ./cmd/serve`) must keep resolving "../../conf/rules.json"
// as the fallback candidate.
func TestLoad_DefaultRulesLegacyCWD(t *testing.T) {
	root := t.TempDir()
	writeRules(t, root, "/w6-legacy")
	launchDir := filepath.Join(root, "cmd", "serve")
	if err := os.MkdirAll(launchDir, 0o755); err != nil {
		t.Fatalf("mkdir launch dir fail: %s", err)
	}

	t.Setenv("RULES_FILE", "")
	t.Chdir(launchDir)

	directives, err := load()
	if err != nil {
		t.Fatalf("load fail: %s", err)
	}
	if len(directives) != 1 || directives[0].Path() != "/w6-legacy" {
		t.Fatalf("expect 1 directive from legacy ../../conf/rules.json, got %d", len(directives))
	}
}

// An explicit RULES_FILE wins verbatim; default probing must never
// shadow the operator's configuration.
func TestLoad_RulesFileEnvWinsOverCandidates(t *testing.T) {
	root := t.TempDir()
	writeRules(t, root, "/w6-candidate")
	explicit := filepath.Join(root, "explicit.json")
	rules := `[{"path":"/w6-explicit","Processors":[` +
		`{"type":"json","data":{"path":"b","type":"create","value":"Mg=="}}]}]`
	if err := os.WriteFile(explicit, []byte(rules), 0o600); err != nil {
		t.Fatalf("write explicit rules fail: %s", err)
	}

	t.Setenv("RULES_FILE", explicit)
	t.Chdir(root)

	directives, err := load()
	if err != nil {
		t.Fatalf("load fail: %s", err)
	}
	if len(directives) != 1 || directives[0].Path() != "/w6-explicit" {
		t.Fatalf("expect the explicit RULES_FILE to win, got %d directives", len(directives))
	}
}

// When no candidate exists (a deployed binary without conf/ next to
// it), resolution falls back to the primary candidate so the read
// error names it — and since #58 (W5) that missing default file is a
// loud error instead of a silent nil.
func TestLoad_DefaultRulesMissingFailsLoud(t *testing.T) {
	t.Setenv("RULES_FILE", "")
	t.Chdir(t.TempDir())

	directives, err := load()
	if err == nil {
		t.Fatalf("expected an error naming the missing default rules file, got directives=%v", directives)
	}
	if !strings.Contains(err.Error(), "conf/rules.json") {
		t.Errorf("error should name the primary candidate conf/rules.json, got: %s", err)
	}
}

// The resolution helper itself: no candidate present → primary default.
func TestRulesFilename_MissingFallsBackToPrimary(t *testing.T) {
	t.Setenv("RULES_FILE", "")
	t.Chdir(t.TempDir())

	if got := rulesFilename(); got != "conf/rules.json" {
		t.Errorf("rulesFilename() = %q, want primary candidate %q", got, "conf/rules.json")
	}
}
