//go:build integration

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/testcontainers/testcontainers-go"
)

func startSuiteBackend(ctx context.Context, leasePath, cdnImage string) (*suiteBackend, error) {
	if err := requireLocalDockerImages(ctx, requiredSuiteBackendImages(cdnImage)); err != nil {
		return nil, err
	}
	kratosBefore, err := dockerContainerIDsForImage(ctx, suiteBackendImages[0])
	if err != nil {
		return nil, err
	}
	spiceBefore, err := dockerContainerIDsForImage(ctx, suiteBackendImages[1])
	if err != nil {
		return nil, err
	}
	if err := flag.Set("geul-integration-lease-file", leasePath); err != nil {
		return nil, fmt.Errorf("select suite integration lease: %w", err)
	}
	hookProxy, err := StartHookProxy("http://" + testcontainers.HostInternal)
	if err != nil {
		return nil, err
	}
	stack, err := testutil.StartBackendIntegrationStackWithOptions(ctx, testutil.BackendIntegrationStackOptions{
		HookBaseURL: hookProxy.DockerBaseURL(),
	})
	if err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = hookProxy.Close(closeCtx)
		cancel()
		return nil, fmt.Errorf("start orchestrator backend stack: %w", err)
	}
	fail := func(err error) (*suiteBackend, error) {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return nil, fmt.Errorf("%w; cleanup: %v", err, errors.Join(stack.Close(), hookProxy.Close(closeCtx)))
	}
	kratosAfter, err := dockerContainerIDsForImage(ctx, suiteBackendImages[0])
	if err != nil {
		return fail(err)
	}
	spiceAfter, err := dockerContainerIDsForImage(ctx, suiteBackendImages[1])
	if err != nil {
		return fail(err)
	}
	if countNewContainerIDs(kratosBefore, kratosAfter) != 1 || countNewContainerIDs(spiceBefore, spiceAfter) != 1 {
		return fail(fmt.Errorf("orchestrator backend must start exactly one Kratos and one SpiceDB container"))
	}
	lease := stack.Lease()
	lease.CDNImage = cdnImage
	lease.HookControlURL = hookProxy.ControlURL()
	lease.HookControlToken = hookProxy.ControlToken()
	return &suiteBackend{
		Lease: lease,
		resetFn: func(ctx context.Context) error {
			return errors.Join(
				hookProxy.Unregister(),
				testutil.ResetBackendIntegrationPackageState(ctx, stack),
			)
		},
		closeFn: func() error {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return errors.Join(stack.Close(), hookProxy.Close(closeCtx))
		},
	}, nil
}

func dockerContainerIDsForImage(ctx context.Context, image string) (map[string]struct{}, error) {
	command := exec.CommandContext(ctx, "docker", "ps", "--filter", "ancestor="+image, "--format", "{{.ID}}")
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list integration containers for %s: %w: %s", image, err, strings.TrimSpace(string(output)))
	}
	ids := make(map[string]struct{})
	for _, id := range strings.Fields(string(output)) {
		ids[id] = struct{}{}
	}
	return ids, nil
}

func countNewContainerIDs(before, after map[string]struct{}) int {
	count := 0
	for id := range after {
		if _, ok := before[id]; !ok {
			count++
		}
	}
	return count
}
