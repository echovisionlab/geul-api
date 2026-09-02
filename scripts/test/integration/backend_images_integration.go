//go:build integration

package main

// Keep the preflight image aligned with testcontainers-go v0.44.0. The
// library's exported alias is deprecated, while its runtime still uses this
// exact image internally.
const suiteReaperImage = "testcontainers/ryuk:0.14.0"

var suiteBackendImages = []string{
	"oryd/kratos:v26.2.0@sha256:2a13bb8d362c7a7ae33bd7c0f5168aee46921f15c916a06346db91c06dc76643",
	"ghcr.io/authzed/spicedb@sha256:c8a558a6cc1f9379fcdcab0171b623d65e7e5f95c998ebb7f937ca00a7c1598c",
	"oryd/oathkeeper:v26.2.0@sha256:467329abde34feefca217b7af76fff59e77fe1795a19376e9d479f33c7c198fc",
	"minio/minio:RELEASE.2025-04-22T22-12-26Z@sha256:a1ea29fa28355559ef137d71fc570e508a214ec84ff8083e39bc5428980b015e",
	"darthsim/imgproxy:v3.31.0@sha256:6db046632f568931e165d61ce289382804f7bbce5b791db6fb6b8d4ace507378",
	suiteReaperImage,
	"testcontainers/sshd:1.4.0",
}

func requiredSuiteBackendImages(cdnImage string) []string {
	images := append([]string(nil), suiteBackendImages...)
	return append(images, cdnImage)
}
