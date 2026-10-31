//go:build integration

package testutil

import (
	"context"
	"fmt"
	"os"
	"time"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	authzed "github.com/authzed/authzed-go/v1"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type integrationSpiceDBBearerToken string

func (token integrationSpiceDBBearerToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(token)}, nil
}

func (integrationSpiceDBBearerToken) RequireTransportSecurity() bool { return false }

const (
	spiceDBIntegrationImage = "ghcr.io/authzed/spicedb@sha256:c8a558a6cc1f9379fcdcab0171b623d65e7e5f95c998ebb7f937ca00a7c1598c"
	spiceDBIntegrationToken = "backend-integration-spicedb-token"
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
	ctr, err := testcontainers.Run(ctx, spiceDBIntegrationImage,
		network.WithNetwork([]string{"spicedb", "geul-identity-spicedb-prod"}, netw),
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
	token = spiceDBIntegrationToken

	raw, err := newIntegrationSpiceDBRawClient(endpoint, token)
	if err != nil {
		_ = cleanupContainer()
		return "", "", nil, nil, err
	}
	if err := writeIntegrationSpiceDBSchema(ctx, raw); err != nil {
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
	if err := seedIntegrationSpiceDBRoleGraph(ctx, client); err != nil {
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

func newIntegrationSpiceDBRawClient(endpoint, token string) (*authzed.Client, error) {
	client, err := authzed.NewClient(endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(integrationSpiceDBBearerToken(token)),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to SpiceDB schema service: %w", err)
	}
	return client, nil
}

func writeIntegrationSpiceDBSchema(ctx context.Context, client *authzed.Client) error {
	schemaPath := AppIntegrationSpiceDBSchemaPath(appIntegrationRepoPath("../.."))
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("read generated SpiceDB schema %s: %w", schemaPath, err)
	}
	if _, err := client.WriteSchema(ctx, &v1.WriteSchemaRequest{Schema: string(schema)}); err != nil {
		return fmt.Errorf("write generated SpiceDB schema: %w", err)
	}
	return nil
}

func seedIntegrationSpiceDBRoleGraph(ctx context.Context, client *auth.SpiceDBClient) error {
	constructors := [...]func() (policyv1.RelationshipMutation, error){
		policyv1.Role.TouchAuthorIncludesAdmin,
		policyv1.Role.TouchUserIncludesAuthor,
		policyv1.Platform.TouchAdminRole,
		policyv1.Platform.TouchAuthorRole,
		policyv1.Platform.TouchUserRole,
	}
	mutations := make([]policyv1.RelationshipMutation, 0, len(constructors))
	for _, construct := range constructors {
		mutation, err := construct()
		if err != nil {
			return fmt.Errorf("construct SpiceDB role graph: %w", err)
		}
		mutations = append(mutations, mutation)
	}
	_, err := client.ApplyRelationships(ctx, mutations...)
	if err != nil {
		return fmt.Errorf("seed SpiceDB role graph: %w", err)
	}
	return nil
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
	return "http://" + oryHostLoopback + ":" + proxyPort.Port(), "http://" + oryHostLoopback + ":" + adminPort.Port(), cleanup, nil
}
