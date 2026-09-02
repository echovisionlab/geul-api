package main

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

func requireLocalDockerImages(ctx context.Context, images []string) error {
	want, err := requiredLocalDockerPlatform(ctx)
	if err != nil {
		return err
	}
	for _, image := range images {
		if err := requireLocalDockerImage(ctx, image, want); err != nil {
			return err
		}
	}
	return nil
}

func requireLocalDockerImage(ctx context.Context, image, want string) error {
	command := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", image)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("required local integration image %s is unavailable: %w: %s", image, err, strings.TrimSpace(string(output)))
	}
	return validateLocalDockerImagePlatform(image, string(output), want)
}

func requiredLocalDockerPlatform(ctx context.Context) (string, error) {
	command := exec.CommandContext(ctx, "docker", "info", "--format", "{{.OSType}}/{{.Architecture}}")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("resolve local Docker platform: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return validateLocalDockerPlatform(string(output), runtime.GOARCH)
}

func validateLocalDockerPlatform(rawPlatform, hostArchitecture string) (string, error) {
	platform := strings.TrimSpace(rawPlatform)
	parts := strings.Split(platform, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("invalid local Docker platform %q", platform)
	}
	architecture := normalizeDockerArchitecture(parts[1])
	if architecture != hostArchitecture {
		return "", fmt.Errorf("local Docker architecture is %s, want native host architecture %s", architecture, hostArchitecture)
	}
	return parts[0] + "/" + architecture, nil
}

func normalizeDockerArchitecture(architecture string) string {
	switch architecture {
	case "aarch64":
		return "arm64"
	case "x86_64":
		return "amd64"
	default:
		return architecture
	}
}

func validateLocalDockerImagePlatform(image, rawPlatform, want string) error {
	platform := strings.TrimSpace(rawPlatform)
	if platform != want {
		return fmt.Errorf("local integration image %s platform is %s, want %s", image, platform, want)
	}
	return nil
}
