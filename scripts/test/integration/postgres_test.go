package main

import (
	"slices"
	"testing"
)

func TestSuitePostgresUsesOneLocalEphemeralContainer(t *testing.T) {
	t.Parallel()

	arguments := suitePostgresDockerArguments(suiteOptions{
		PostgresImage: "local-postgres@sha256:test",
	}, "geul_it_template_test")
	for _, required := range []string{"--pull=never", "--rm", "--tmpfs", "/var/lib/postgresql:rw"} {
		if !slices.Contains(arguments, required) {
			t.Fatalf("docker arguments %q do not contain %q", arguments, required)
		}
	}
	if got := arguments[len(arguments)-1]; got != "local-postgres@sha256:test" {
		t.Fatalf("image argument = %q", got)
	}
	if slices.Contains(arguments, "--platform") {
		t.Fatalf("docker arguments must not override the local image platform: %q", arguments)
	}
}

func TestTerminalPostgresContainerStatus(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"dead", "exited", " exited\n"} {
		if !isTerminalPostgresContainerStatus(status) {
			t.Fatalf("status %q is not terminal", status)
		}
	}
	for _, status := range []string{"", "created", "running", "restarting"} {
		if isTerminalPostgresContainerStatus(status) {
			t.Fatalf("status %q is terminal", status)
		}
	}
}
