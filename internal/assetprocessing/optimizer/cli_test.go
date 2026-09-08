package optimizer

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestCLI(t *testing.T) {
	originalCommandContext := commandContext
	t.Cleanup(func() { commandContext = originalCommandContext })

	cli := NewCLI("gltf-transform", "node", "scripts/optimize-particle-mesh.mjs")
	commandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "printf 'OVERVIEW\\nkey,value\\nversion,2.0\\n'")
	}
	inspection, err := cli.Inspect(context.Background(), "model.glb")
	if err != nil || inspection.Version != "2.0" {
		t.Fatalf("inspect: %#v, %v", inspection, err)
	}
	if err := cli.Optimize(context.Background(), "in.glb", "out.glb", Options{TargetRatioPercent: 50, Profile: ProfileDracoWebPV1}); err != nil {
		t.Fatalf("optimize: %v", err)
	}
	if err := cli.Optimize(context.Background(), "in.glb", "out.glb", Options{TargetRatioPercent: 50, Profile: ProfileParticleMeshV1}); err != nil {
		t.Fatalf("optimize particle mesh: %v", err)
	}
	if err := cli.Optimize(context.Background(), "in.glb", "out.glb", Options{TargetRatioPercent: 50, Profile: "unknown"}); err == nil {
		t.Fatal("expected unknown profile error")
	}

	commandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "printf '\"unterminated,'")
	}
	if _, err := cli.Inspect(context.Background(), "model.glb"); err == nil {
		t.Fatal("expected inspect parse error")
	}

	commandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "printf 'cli failed' >&2; exit 7")
	}
	if _, err := cli.Inspect(context.Background(), "model.glb"); err == nil || !strings.Contains(err.Error(), "cli failed") {
		t.Fatalf("expected inspect CLI error with stderr, got %v", err)
	}
	if err := cli.Optimize(context.Background(), "in.glb", "out.glb", Options{TargetRatioPercent: 50, Profile: ProfileDracoWebPV1}); err == nil || !strings.Contains(err.Error(), "cli failed") {
		t.Fatalf("expected CLI error with stderr, got %v", err)
	}
	if err := cli.Optimize(context.Background(), "in.glb", "out.glb", Options{TargetRatioPercent: 50, Profile: ProfileParticleMeshV1}); err == nil || !strings.Contains(err.Error(), "cli failed") {
		t.Fatalf("expected particle CLI error with stderr, got %v", err)
	}
}

func TestCLISelectsProfileCommand(t *testing.T) {
	originalCommandContext := commandContext
	t.Cleanup(func() { commandContext = originalCommandContext })

	cli := NewCLI("gltf-transform-bin", "node-bin", "particle-script.mjs")
	type invocation struct {
		binary string
		args   []string
	}
	var invocations []invocation
	commandContext = func(ctx context.Context, binary string, args ...string) *exec.Cmd {
		invocations = append(invocations, invocation{binary: binary, args: append([]string(nil), args...)})
		return exec.CommandContext(ctx, "sh", "-c", "true")
	}

	if err := cli.Optimize(context.Background(), "in.glb", "out.glb", Options{TargetRatioPercent: 50, Profile: ProfileDracoWebPV1}); err != nil {
		t.Fatalf("generic optimize: %v", err)
	}
	if err := cli.Optimize(context.Background(), "in.glb", "out.glb", Options{TargetRatioPercent: 100, Profile: ProfileParticleMeshV1}); err != nil {
		t.Fatalf("particle optimize: %v", err)
	}

	if len(invocations) != 2 {
		t.Fatalf("invocation count: %d", len(invocations))
	}
	if invocations[0].binary != "gltf-transform-bin" || len(invocations[0].args) == 0 || invocations[0].args[0] != "optimize" {
		t.Fatalf("generic invocation: %#v", invocations[0])
	}
	wantParticleArgs := []string{"particle-script.mjs", "in.glb", "out.glb", "--simplify", "false"}
	if invocations[1].binary != "node-bin" || strings.Join(invocations[1].args, "\x00") != strings.Join(wantParticleArgs, "\x00") {
		t.Fatalf("particle invocation: %#v", invocations[1])
	}
}
