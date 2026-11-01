//go:build integration

package orytest

import (
	"context"
	"fmt"
	"os"

	pgtest "github.com/echovisionlab/geul-api/internal/testutil/pgtest"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"

	authzed "github.com/authzed/authzed-go/v1"
	"github.com/echovisionlab/geul-api/internal/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type integrationSpiceDBBearerToken string

func (token integrationSpiceDBBearerToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(token)}, nil
}

func (integrationSpiceDBBearerToken) RequireTransportSecurity() bool { return false }

const (
	SpiceDBIntegrationImage = "ghcr.io/authzed/spicedb@sha256:c8a558a6cc1f9379fcdcab0171b623d65e7e5f95c998ebb7f937ca00a7c1598c"
	SpiceDBIntegrationToken = "backend-integration-spicedb-token"
)

func NewIntegrationSpiceDBRawClient(endpoint, token string) (*authzed.Client, error) {
	client, err := authzed.NewClient(endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(integrationSpiceDBBearerToken(token)),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to SpiceDB schema service: %w", err)
	}
	return client, nil
}

func WriteIntegrationSpiceDBSchema(ctx context.Context, client *authzed.Client) error {
	schemaPath := pgtest.AppIntegrationSpiceDBSchemaPath(pgtest.AppIntegrationRepoPath("../.."))
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("read generated SpiceDB schema %s: %w", schemaPath, err)
	}
	if _, err := client.WriteSchema(ctx, &v1.WriteSchemaRequest{Schema: string(schema)}); err != nil {
		return fmt.Errorf("write generated SpiceDB schema: %w", err)
	}
	return nil
}

func SeedIntegrationSpiceDBRoleGraph(ctx context.Context, client *auth.SpiceDBClient) error {
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
