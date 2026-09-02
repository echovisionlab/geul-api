package auth

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	authzed "github.com/authzed/authzed-go/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type spiceDBBearerToken struct {
	value                    string
	requireTransportSecurity bool
}

func (token spiceDBBearerToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + token.value}, nil
}

func (token spiceDBBearerToken) RequireTransportSecurity() bool {
	return token.requireTransportSecurity
}

// SpiceDBClient is the sole API authorization transport. It intentionally
// exposes no raw subject or database-permission fallback to consumers.
type SpiceDBClient struct {
	client v1.PermissionsServiceClient
	close  func() error
}

// NewSpiceDBClient uses the official generated gRPC client. TLS is the default;
// insecure transport is opt-in for local/internal deployments only.
func NewSpiceDBClient(endpoint, apiToken string, allowInsecure bool) (*SpiceDBClient, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("SpiceDB endpoint is required")
	}
	apiToken = strings.TrimSpace(apiToken)
	if apiToken == "" {
		return nil, fmt.Errorf("SpiceDB API token is required")
	}
	token := spiceDBBearerToken{
		value:                    apiToken,
		requireTransportSecurity: !allowInsecure,
	}
	dialOptions := []grpc.DialOption{grpc.WithPerRPCCredentials(token)}
	if allowInsecure {
		dialOptions = append(dialOptions, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		dialOptions = append(dialOptions, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS12,
		})))
	}
	client, err := authzed.NewClient(endpoint, dialOptions...)
	if err != nil {
		return nil, fmt.Errorf("connect to SpiceDB: %w", err)
	}
	return &SpiceDBClient{client: client.PermissionsServiceClient, close: client.Close}, nil
}

func (c *SpiceDBClient) Close() error {
	if c == nil || c.close == nil {
		return nil
	}
	return c.close()
}
