package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"slices"
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
)

type IdentityGetter interface {
	GetIdentity(ctx context.Context, identityID string) (*Identity, error)
	GetIdentityWithIncludeCredential(ctx context.Context, identityID, credentialType string) (*Identity, error)
}

type IdentityLister interface {
	ListIdentities(ctx context.Context, page, perPage int) ([]*Identity, int64, error)
}

type IdentityCredentialFinder interface {
	FindIdentityByCredentialIdentifier(ctx context.Context, identifier string) (*Identity, bool, error)
}

type IdentityEmailReader interface {
	GetIdentityEmail(ctx context.Context, identityID string) (string, error)
}

type IdentityReader interface {
	IdentityGetter
	IdentityLister
	IdentityEmailReader
}

type IdentityTraitWriter interface {
	UpdateIdentityTraits(ctx context.Context, identityID string, traits structured.Fields) error
}

type IdentityCreator interface {
	CreateIdentity(ctx context.Context, identity *Identity) (*Identity, error)
}

type IdentityExternalIDWriter interface {
	UpdateIdentityExternalID(ctx context.Context, identityID string, externalID string) error
}

type IdentityAccountEmailStateWriter interface {
	UpdateIdentityAccountEmailState(ctx context.Context, identityID string, currentEmail *string, traits structured.Fields, addresses []VerifiableAddress) error
}

type IdentityVerifiableAddressWriter interface {
	UpdateIdentityVerifiableAddresses(ctx context.Context, identityID string, addresses []VerifiableAddress) error
}

type IdentityMetadataAdminWriter interface {
	UpdateIdentityMetadataAdmin(ctx context.Context, identityID string, metadataAdmin structured.Fields) error
}

type IdentityStateWriter interface {
	SetIdentityState(ctx context.Context, identityID, state string) error
}

type IdentitySessionRevoker interface {
	DeleteIdentitySessions(ctx context.Context, identityID string) error
}

type SessionRevoker interface {
	DeleteSession(ctx context.Context, sessionID string) error
}

type IdentityCredentialIdentifierDeleter interface {
	DeleteIdentityCredentialIdentifier(ctx context.Context, identityID, credentialType, identifier string) error
}

type IdentityDeleter interface {
	DeleteIdentity(ctx context.Context, identityID string) error
}

// IdentityManager defines the full interface for managing Kratos identities.
type IdentityManager interface {
	IdentityReader
	IdentityTraitWriter
	IdentityVerifiableAddressWriter
	IdentityMetadataAdminWriter
	IdentityStateWriter
	IdentitySessionRevoker
	IdentityDeleter
}

// KratosClient provides methods to interact with Kratos Admin API.
type KratosClient struct {
	adminURL   string
	httpClient *http.Client
}

const maxKratosErrorBodyBytes = 64 << 10

type KratosHTTPError struct {
	StatusCode int
	Body       string
}

func (e *KratosHTTPError) Error() string {
	return fmt.Sprintf("kratos returned %d: %s", e.StatusCode, e.Body)
}

func IsKratosConflict(err error) bool {
	var httpErr *KratosHTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusConflict
}

func (c *KratosClient) doAdminRequest(
	ctx context.Context,
	method string,
	endpoint string,
	body io.Reader,
	contentType string,
	operation string,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("create %s request: %w", operation, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	return resp, nil
}

func kratosResponseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxKratosErrorBodyBytes))
	return &KratosHTTPError{StatusCode: resp.StatusCode, Body: string(body)}
}

func kratosStatusAccepted(status int, accepted ...int) bool {
	return slices.Contains(accepted, status)
}

// NewKratosClient creates a new KratosClient.
func NewKratosClient(adminURL string) *KratosClient {
	return &KratosClient{
		adminURL: adminURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// GetIdentity retrieves an identity by ID.
func (c *KratosClient) GetIdentity(ctx context.Context, identityID string) (*Identity, error) {
	return c.getIdentity(ctx, identityID, "")
}

// GetIdentityWithIncludeCredential retrieves an identity and includes requested credential config (e.g. "oidc").
func (c *KratosClient) GetIdentityWithIncludeCredential(ctx context.Context, identityID, credentialType string) (*Identity, error) {
	return c.getIdentity(ctx, identityID, credentialType)
}

func (c *KratosClient) getIdentity(ctx context.Context, identityID, credentialType string) (*Identity, error) {
	endpoint := fmt.Sprintf("%s/admin/identities/%s", c.adminURL, identityID)
	if credentialType != "" {
		endpoint = fmt.Sprintf("%s?include_credential=%s", endpoint, neturl.QueryEscape(credentialType))
	}

	resp, err := c.doAdminRequest(ctx, http.MethodGet, endpoint, nil, "", "get identity")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("identity not found: %s", identityID)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, kratosResponseError(resp)
	}

	var identity Identity
	if err := json.NewDecoder(resp.Body).Decode(&identity); err != nil {
		return nil, fmt.Errorf("failed to decode identity: %w", err)
	}

	return &identity, nil
}

// ListIdentities retrieves a paginated list of identities.
func (c *KratosClient) ListIdentities(ctx context.Context, page, perPage int) ([]*Identity, int64, error) {
	url := fmt.Sprintf("%s/admin/identities?page=%d&per_page=%d", c.adminURL, page, perPage)

	resp, err := c.doAdminRequest(ctx, http.MethodGet, url, nil, "", "list identities")
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, kratosResponseError(resp)
	}

	var identities []*Identity
	if err := json.NewDecoder(resp.Body).Decode(&identities); err != nil {
		return nil, 0, fmt.Errorf("failed to decode identities: %w", err)
	}

	var total int64
	if totalStr := resp.Header.Get("X-Total-Count"); totalStr != "" {
		fmt.Sscanf(totalStr, "%d", &total)
	} else {
		total = int64(len(identities))
	}

	return identities, total, nil
}

// FindIdentityByCredentialIdentifier resolves one exact credential identifier
// without enumerating the identity collection in application code. A missing
// identity is a normal result; more than one match is rejected because the
// identifier is expected to be unique at the authentication boundary.
func (c *KratosClient) FindIdentityByCredentialIdentifier(
	ctx context.Context,
	identifier string,
) (*Identity, bool, error) {
	normalized := normalizeIdentityEmail(identifier)
	if normalized == "" {
		return nil, false, errors.New("credential identifier is required")
	}

	endpoint, err := neturl.Parse(c.adminURL + "/admin/identities")
	if err != nil {
		return nil, false, fmt.Errorf("parse Kratos Admin URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("credentials_identifier", normalized)
	query.Set("page_size", "2")
	endpoint.RawQuery = query.Encode()

	resp, err := c.doAdminRequest(
		ctx,
		http.MethodGet,
		endpoint.String(),
		nil,
		"",
		"find identity by credential identifier",
	)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, false, kratosResponseError(resp)
	}

	var identities []*Identity
	if err := json.NewDecoder(resp.Body).Decode(&identities); err != nil {
		return nil, false, fmt.Errorf("decode identity credential lookup: %w", err)
	}
	switch len(identities) {
	case 0:
		return nil, false, nil
	case 1:
		return identities[0], true, nil
	default:
		return nil, false, errors.New("credential identifier resolved to multiple identities")
	}
}

// GetIdentityEmail retrieves the email address from a Kratos identity's traits.
// Returns empty string if identity not found or email trait doesn't exist.
func (c *KratosClient) GetIdentityEmail(ctx context.Context, identityID string) (string, error) {
	identity, err := c.GetIdentity(ctx, identityID)
	if err != nil {
		return "", err
	}

	if identity.Traits == nil {
		return "", nil
	}

	email, ok := identity.Traits["email"].(string)
	if !ok {
		return "", nil
	}

	return email, nil
}
