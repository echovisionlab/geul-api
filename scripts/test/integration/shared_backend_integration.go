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

func startSuiteBackend(ctx context.Context, leasePath string) (*suiteBackend, error) {
	backendImages := suiteBackendImages()
	if err := requireLocalDockerImages(ctx, backendImages); err != nil {
		return nil, err
	}
	if err := flag.Set("geul-integration-lease-file", leasePath); err != nil {
		return nil, fmt.Errorf("select suite integration lease: %w", err)
	}
	hookProxy, err := StartHookProxy(ctx, "http://"+testcontainers.HostInternal)
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
	// Count only this stack's network: other suite runs may share the daemon.
	kratos, err := dockerContainerIDsForImage(ctx, backendImages[0], stack.Network.Name)
	if err != nil {
		return fail(err)
	}
	spiceDB, err := dockerContainerIDsForImage(ctx, backendImages[1], stack.Network.Name)
	if err != nil {
		return fail(err)
	}
	if len(kratos) != 1 || len(spiceDB) != 1 {
		return fail(fmt.Errorf("orchestrator backend must start exactly one Kratos and one SpiceDB container"))
	}
	lease := stack.Lease()
	lease.HookControlURL = hookProxy.ControlURL()
	lease.HookControlToken = hookProxy.ControlToken()
	return &suiteBackend{
		Lease: lease,
		// Tests own their databases, SpiceDB datastores and buckets, so only the
		// single hook upstream registration needs releasing between packages.
		resetFn: func(context.Context) error {
			return hookProxy.Unregister()
		},
		closeFn: func() error {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return errors.Join(stack.Close(), hookProxy.Close(closeCtx))
		},
	}, nil
}

func dockerContainerIDsForImage(ctx context.Context, image, network string) (map[string]struct{}, error) {
	command := exec.CommandContext(ctx, "docker", "ps", "--filter", "ancestor="+image, "--filter", "network="+network, "--format", "{{.ID}}")
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
