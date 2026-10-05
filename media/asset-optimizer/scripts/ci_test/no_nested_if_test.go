package citest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoNestedIfRejectsNestedIf(t *testing.T) {
	output, err := lint(t, "fixture.go", "package fixture\nfunc check(a, b bool) { if a { if b {} } }\n")
	if err == nil {
		t.Fatal("expected nested if to fail linting")
	}
	if !strings.Contains(output, "nested if statement") {
		t.Fatalf("expected nested-if diagnostic, got %q", output)
	}
}

func TestNoNestedIfAllowsGuardClausesAndElseIf(t *testing.T) {
	output, err := lint(t, "fixture.go", "package fixture\nfunc check(a, b bool) { if !a { return }; if b {} else if a {} }\n")
	if err != nil {
		t.Fatalf("expected valid control flow to pass: %v\n%s", err, output)
	}
}

func TestNoNestedIfChecksTestFiles(t *testing.T) {
	output, err := lint(t, "fixture_test.go", "package fixture\nfunc check(a, b bool) { if a { if b {} } }\n")
	if err == nil {
		t.Fatal("expected nested if in test file to fail linting")
	}
	if !strings.Contains(output, "fixture_test.go") {
		t.Fatalf("expected test filename in diagnostic, got %q", output)
	}
}

func lint(t *testing.T, filename, source string) (string, error) {
	t.Helper()

	fixtureDirectory := t.TempDir()
	fixturePath := filepath.Join(fixtureDirectory, filename)
	if err := os.WriteFile(fixturePath, []byte(source), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	command := exec.Command("go", "run", "../ci/no-nested-if.go", fixtureDirectory)
	output, err := command.CombinedOutput()
	return string(output), err
}
