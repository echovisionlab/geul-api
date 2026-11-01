//go:build integration

package runtimetest

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	orytest "github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// runBackendIntegrationSpiceDB starts the same authorization service used by
// the runtime and writes the generated event-contracts schema before exposing
// the client to tests. The in-memory datastore is deliberate: PostgreSQL owns
// product and Kratos state, while this test boundary verifies SpiceDB schema
// and relationship behavior without introducing a second datastore migration.
func runBackendIntegrationSpiceDB(
	ctx context.Context,
	netw *testcontainers.DockerNetwork,
) (endpoint, token string, client *auth.SpiceDBClient, cleanup func() error, err error) {
	ctr, err := testcontainers.Run(ctx, orytest.SpiceDBIntegrationImage, network.WithNetwork([]string{"spicedb", "geul-identity-spicedb-prod"}, netw),
		testcontainers.WithExposedPorts("50051/tcp"),
		testcontainers.WithCmd("serve-testing", "--grpc-addr", "0.0.0.0:50051"),
		testcontainers.WithWaitStrategy(wait.ForLog("grpc server started serving").WithStartupTimeout(2*time.Minute)),
	)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("start SpiceDB container: %w", err)
	}

	cleanupContainer := func() error {
		return runBackendIntegrationBoundedCleanup(func(ctx context.Context) error {
			return ctr.Terminate(ctx)
		})
	}
	port, err := ctr.MappedPort(ctx, "50051/tcp")
	if err != nil {
		_ = cleanupContainer()
		return "", "", nil, nil, fmt.Errorf("resolve SpiceDB gRPC port: %w", err)
	}
	endpoint = "127.0.0.1:" + port.Port()
	token = orytest.SpiceDBIntegrationToken

	raw, err := orytest.NewIntegrationSpiceDBRawClient(endpoint, token)
	if err != nil {
		_ = cleanupContainer()
		return "", "", nil, nil, err
	}
	if err := orytest.WriteIntegrationSpiceDBSchema(ctx, raw); err != nil {
		_ = raw.Close()
		_ = cleanupContainer()
		return "", "", nil, nil, err
	}
	client, err = auth.NewSpiceDBClient(endpoint, token, true)
	if err != nil {
		_ = raw.Close()
		_ = cleanupContainer()
		return "", "", nil, nil, fmt.Errorf("create SpiceDB integration client: %w", err)
	}
	if err := orytest.SeedIntegrationSpiceDBRoleGraph(ctx, client); err != nil {
		_ = client.Close()
		_ = raw.Close()
		_ = cleanupContainer()
		return "", "", nil, nil, err
	}
	if err := raw.Close(); err != nil {
		_ = client.Close()
		_ = cleanupContainer()
		return "", "", nil, nil, fmt.Errorf("close SpiceDB schema client: %w", err)
	}
	cleanup = func() error {
		closeErr := client.Close()
		terminateErr := cleanupContainer()
		if closeErr != nil {
			return closeErr
		}
		return terminateErr
	}
	return endpoint, token, client, cleanup, nil
}

func runBackendIntegrationOathkeeper(
	ctx context.Context,
	netw *testcontainers.DockerNetwork,
) (proxyURL, adminURL string, cleanup func() error, err error) {
	configDirectory, configFiles, err := prepareOathkeeperIntegrationFiles()
	if err != nil {
		return "", "", nil, err
	}
	ctr, err := testcontainers.Run(ctx, "oryd/oathkeeper:v26.2.0@sha256:467329abde34feefca217b7af76fff59e77fe1795a19376e9d479f33c7c198fc",
		network.WithNetwork([]string{"oathkeeper", "geul-identity-oathkeeper-prod"}, netw),
		testcontainers.WithFiles(configFiles...),
		testcontainers.WithExposedPorts("4455/tcp", "4456/tcp"),
		testcontainers.WithEnv(map[string]string{"TOKEN_SIGNING_SECRET": IntegrationTokenSigningSecret}),
		testcontainers.WithCmd("serve", "-c", "/etc/oathkeeper/oathkeeper.yml"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/health/ready").WithPort("4456/tcp").WithStartupTimeout(2*time.Minute)),
	)
	if err != nil {
		_ = os.RemoveAll(configDirectory)
		return "", "", nil, fmt.Errorf("start Oathkeeper container: %w", err)
	}
	cleanup = func() error {
		cleanupErr := runBackendIntegrationBoundedCleanup(func(ctx context.Context) error {
			return ctr.Terminate(ctx)
		})
		if err := os.RemoveAll(configDirectory); cleanupErr == nil {
			cleanupErr = err
		}
		return cleanupErr
	}
	proxyPort, err := ctr.MappedPort(ctx, "4455/tcp")
	if err != nil {
		_ = cleanup()
		return "", "", nil, fmt.Errorf("resolve Oathkeeper proxy port: %w", err)
	}
	adminPort, err := ctr.MappedPort(ctx, "4456/tcp")
	if err != nil {
		_ = cleanup()
		return "", "", nil, fmt.Errorf("resolve Oathkeeper admin port: %w", err)
	}
	return "http://" + orytest.OryHostLoopback + ":" + proxyPort.Port(), "http://" + orytest.OryHostLoopback + ":" + adminPort.Port(), cleanup, nil
}
