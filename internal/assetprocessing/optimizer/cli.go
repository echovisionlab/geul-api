package optimizer

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type CLI struct {
	gltfTransformPath      string
	nodePath               string
	particleMeshScriptPath string
}

var commandContext = exec.CommandContext

func NewCLI(gltfTransformPath, nodePath, particleMeshScriptPath string) *CLI {
	return &CLI{
		gltfTransformPath:      gltfTransformPath,
		nodePath:               nodePath,
		particleMeshScriptPath: particleMeshScriptPath,
	}
}

func (c *CLI) Inspect(ctx context.Context, path string) (Inspection, error) {
	output, err := c.run(ctx, c.gltfTransformPath, "inspect", path, "--format", "csv")
	if err != nil {
		return Inspection{}, err
	}
	return ParseInspectCSV(output)
}

func (c *CLI) Optimize(ctx context.Context, inputPath, outputPath string, options Options) error {
	switch options.Profile {
	case ProfileDracoWebPV1:
		_, err := c.run(ctx, c.gltfTransformPath, BuildOptimizeArgs(inputPath, outputPath, options)...)
		return err
	case ProfileParticleMeshV1:
		args := append([]string{c.particleMeshScriptPath}, BuildParticleMeshArgs(inputPath, outputPath, options)...)
		_, err := c.run(ctx, c.nodePath, args...)
		return err
	default:
		return fmt.Errorf("unsupported optimization profile %q", options.Profile)
	}
}

func (c *CLI) run(ctx context.Context, binaryPath string, args ...string) (string, error) {
	cmd := commandContext(ctx, binaryPath, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("run %s %s: %w: %s", binaryPath, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
