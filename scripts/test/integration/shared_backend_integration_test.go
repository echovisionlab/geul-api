//go:build integration

package main

import (
	"github.com/echovisionlab/geul-api/internal/testutil"
	"runtime"
	"slices"
	"testing"
)

func TestSuiteBackendPreflightsEveryRuntimeImage(t *testing.T) {
	t.Parallel()
	want := []string{
		testutil.KratosIntegrationImage(),
		"ghcr.io/authzed/spicedb@sha256:c8a558a6cc1f9379fcdcab0171b623d65e7e5f95c998ebb7f937ca00a7c1598c",
		"oryd/oathkeeper:v26.2.0@sha256:467329abde34feefca217b7af76fff59e77fe1795a19376e9d479f33c7c198fc",
		"minio/minio:RELEASE.2025-04-22T22-12-26Z@sha256:a1ea29fa28355559ef137d71fc570e508a214ec84ff8083e39bc5428980b015e",
		"darthsim/imgproxy:v3.31.0@sha256:6db046632f568931e165d61ce289382804f7bbce5b791db6fb6b8d4ace507378",
		suiteReaperImage,
		"testcontainers/sshd:1.4.0@sha256:bdae17f702908bee93c877ab3e97eddd782e2fb5bdd23a9f29869b9a87b30acd",
	}
	if got := requiredSuiteBackendImages(); !slices.Equal(got, want) {
		t.Fatalf("suite backend images = %q, want %q", got, want)
	}
}

func TestValidateLocalDockerImagePlatformRequiresNativeImage(t *testing.T) {
	t.Parallel()
	want := "linux/" + runtime.GOARCH
	if err := validateLocalDockerImagePlatform("image:test", want+"\n", want); err != nil {
		t.Fatalf("validate native image: %v", err)
	}
	if err := validateLocalDockerImagePlatform("image:test", "linux/other", want); err == nil {
		t.Fatal("validate non-native image succeeded")
	}
}

func TestValidateLocalDockerPlatformUsesDaemonOSAndNormalizesArchitecture(t *testing.T) {
	t.Parallel()
	platform, err := validateLocalDockerPlatform("linux/aarch64\n", "arm64")
	if err != nil {
		t.Fatalf("validate macOS-hosted Docker platform: %v", err)
	}
	if platform != "linux/arm64" {
		t.Fatalf("normalized Docker platform = %q", platform)
	}
	if _, err := validateLocalDockerPlatform("linux/x86_64", "arm64"); err == nil {
		t.Fatal("validate emulated Docker architecture succeeded")
	}
}
