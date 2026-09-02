package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"strings"

	"github.com/echovisionlab/geul-api/internal/structured"
)

func (c *KratosClient) patchIdentity(ctx context.Context, identityID string, patch []structured.Fields) error {
	jsonPayload, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch: %w", err)
	}

	url := fmt.Sprintf("%s/admin/identities/%s", c.adminURL, identityID)
	resp, err := c.doAdminRequest(
		ctx,
		http.MethodPatch,
		url,
		bytes.NewReader(jsonPayload),
		"application/json-patch+json",
		"patch identity",
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return kratosResponseError(resp)
	}

	return nil
}

func identityTraitPatch(identity *Identity, traits structured.Fields) []structured.Fields {
	patch := make([]structured.Fields, 0, len(traits))
	for key, value := range traits {
		path := "/traits/" + escapeJSONPointer(key)
		if value == nil {
			if identityHasTrait(identity, key) {
				patch = append(patch, structured.Fields{"op": "remove", "path": path})
			}
			continue
		}
		patch = append(patch, structured.Fields{
			"op":    "add",
			"path":  path,
			"value": value,
		})
	}
	return patch
}

func identityHasTrait(identity *Identity, key string) bool {
	if identity == nil || identity.Traits == nil {
		return false
	}
	_, exists := identity.Traits[key]
	return exists
}

// UpdateIdentityTraits updates the traits for an identity.
func (c *KratosClient) UpdateIdentityTraits(ctx context.Context, identityID string, traits structured.Fields) error {
	for _, protectedTrait := range []string{"email", "pending_email"} {
		if _, ok := traits[protectedTrait]; ok {
			return fmt.Errorf("identity %s must be changed through the account email lifecycle", protectedTrait)
		}
	}

	// Patch individual trait fields instead of replacing the whole identity or
	// traits object. Replacing traits can cause Kratos to rebuild verifiable
	// addresses and mark an unchanged email as unverified.
	identity, err := c.GetIdentity(ctx, identityID)
	if err != nil {
		return err
	}

	patch := identityTraitPatch(identity, traits)
	if len(patch) == 0 {
		return nil
	}

	return c.patchIdentity(ctx, identityID, patch)
}

func (c *KratosClient) UpdateIdentityAccountEmailState(ctx context.Context, identityID string, currentEmail *string, traits structured.Fields, addresses []VerifiableAddress) error {
	identity, err := c.GetIdentity(ctx, identityID)
	if err != nil {
		return err
	}

	if _, containsEmail := traits["email"]; containsEmail {
		return fmt.Errorf("use currentEmail argument to change identity email")
	}
	patch := make([]structured.Fields, 0, len(traits)+2)
	if currentEmail != nil {
		patch = append(patch, structured.Fields{
			"op":    "add",
			"path":  "/traits/email",
			"value": strings.TrimSpace(*currentEmail),
		})
	}
	patch = append(patch, identityTraitPatch(identity, traits)...)
	if addresses != nil {
		patch = append(patch, structured.Fields{
			"op":    "add",
			"path":  "/verifiable_addresses",
			"value": addresses,
		})
	}
	if len(patch) == 0 {
		return nil
	}

	return c.patchIdentity(ctx, identityID, patch)
}

func (c *KratosClient) UpdateIdentityVerifiableAddresses(ctx context.Context, identityID string, addresses []VerifiableAddress) error {
	return c.patchIdentity(ctx, identityID, []structured.Fields{{
		"op":    "add",
		"path":  "/verifiable_addresses",
		"value": addresses,
	}})
}

func escapeJSONPointer(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")
	value = strings.ReplaceAll(value, "/", "~1")
	return value
}

// UpdateIdentityMetadataAdmin updates the metadata_admin for an identity (for ban status).
func (c *KratosClient) UpdateIdentityMetadataAdmin(ctx context.Context, identityID string, metadataAdmin structured.Fields) error {
	return c.patchIdentity(ctx, identityID, []structured.Fields{
		{"op": "replace", "path": "/metadata_admin", "value": metadataAdmin},
	})
}

// UpdateIdentityMetadata updates the metadata_public for an identity.
func (c *KratosClient) UpdateIdentityMetadata(ctx context.Context, identityID string, metadataPublic structured.Fields) error {
	return c.patchIdentity(ctx, identityID, []structured.Fields{
		{"op": "replace", "path": "/metadata_public", "value": metadataPublic},
	})
}

// UpdateIdentityExternalID sets Geul's Member pointer through the Kratos Admin
// API. Browser input and Kratos traits never own this field.
func (c *KratosClient) UpdateIdentityExternalID(ctx context.Context, identityID string, externalID string) error {
	return c.patchIdentity(ctx, identityID, []structured.Fields{{
		"op":    "add",
		"path":  "/external_id",
		"value": externalID,
	}})
}

// SetIdentityState changes identity state (active/inactive).
func (c *KratosClient) SetIdentityState(ctx context.Context, identityID, state string) error {
	return c.patchIdentity(ctx, identityID, []structured.Fields{
		{
			"op":    "replace",
			"path":  "/state",
			"value": state,
		},
	})
}

// DeleteIdentitySessions revokes all sessions for an identity.
func (c *KratosClient) DeleteIdentitySessions(ctx context.Context, identityID string) error {
	url := fmt.Sprintf("%s/admin/identities/%s/sessions", c.adminURL, identityID)
	resp, err := c.doAdminRequest(ctx, http.MethodDelete, url, nil, "", "delete identity sessions")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Kratos returns 404 when the identity has no session collection to revoke.
	// That is already the desired idempotent state.
	if !kratosStatusAccepted(resp.StatusCode, http.StatusOK, http.StatusNoContent, http.StatusNotFound) {
		return kratosResponseError(resp)
	}

	return nil
}

func (c *KratosClient) DeleteSession(ctx context.Context, sessionID string) error {
	url := fmt.Sprintf("%s/admin/sessions/%s", c.adminURL, sessionID)
	resp, err := c.doAdminRequest(ctx, http.MethodDelete, url, nil, "", "delete session")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if !kratosStatusAccepted(resp.StatusCode, http.StatusOK, http.StatusNoContent, http.StatusNotFound) {
		return kratosResponseError(resp)
	}
	return nil
}

// CreateIdentity creates a passwordless Kratos identity.
func (c *KratosClient) CreateIdentity(ctx context.Context, identity *Identity) (*Identity, error) {
	if identity == nil {
		return nil, fmt.Errorf("identity is required")
	}
	if strings.TrimSpace(identity.SchemaID) == "" {
		return nil, fmt.Errorf("identity schema_id is required")
	}
	if identity.Traits == nil {
		return nil, fmt.Errorf("identity traits are required")
	}

	state := strings.TrimSpace(identity.State)
	if state == "" {
		state = KratosStateActive
	}
	payload := structured.Fields{
		"schema_id": identity.SchemaID,
		"traits":    identity.Traits,
		"state":     state,
	}
	if identity.MetadataPublic != nil {
		payload["metadata_public"] = identity.MetadataPublic
	}
	if identity.MetadataAdmin != nil {
		payload["metadata_admin"] = identity.MetadataAdmin
	}
	if len(identity.VerifiableAddresses) > 0 {
		payload["verifiable_addresses"] = identity.VerifiableAddresses
	}
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal identity create request: %w", err)
	}

	url := fmt.Sprintf("%s/admin/identities", c.adminURL)
	resp, err := c.doAdminRequest(
		ctx,
		http.MethodPost,
		url,
		bytes.NewReader(jsonPayload),
		"application/json",
		"create identity",
	)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, kratosResponseError(resp)
	}

	var created Identity
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return nil, fmt.Errorf("failed to decode created identity: %w", err)
	}
	return &created, nil
}

// DeleteIdentityCredentialIdentifier removes one identifier from a credential.
// Ory supports this for OIDC/SAML credentials through the identifier query
// parameter on the credential delete endpoint.
func (c *KratosClient) DeleteIdentityCredentialIdentifier(ctx context.Context, identityID, credentialType, identifier string) error {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return fmt.Errorf("credential identifier is required")
	}
	url := fmt.Sprintf(
		"%s/admin/identities/%s/credentials/%s?identifier=%s",
		c.adminURL,
		neturl.PathEscape(identityID),
		neturl.PathEscape(credentialType),
		neturl.QueryEscape(identifier),
	)
	resp, err := c.doAdminRequest(
		ctx,
		http.MethodDelete,
		url,
		nil,
		"",
		"delete identity credential identifier",
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if kratosStatusAccepted(
		resp.StatusCode,
		http.StatusOK,
		http.StatusNoContent,
		http.StatusNotFound,
	) {
		return nil
	}
	return kratosResponseError(resp)
}

// DeleteIdentity permanently deletes a Kratos identity.
// This should only be called after the grace period for account deletion has passed.
func (c *KratosClient) DeleteIdentity(ctx context.Context, identityID string) error {
	url := fmt.Sprintf("%s/admin/identities/%s", c.adminURL, identityID)
	resp, err := c.doAdminRequest(ctx, http.MethodDelete, url, nil, "", "delete identity")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Deletion is idempotent so a durable cleanup retry can safely resume after
	// the identity was deleted but before local progress was persisted.
	if !kratosStatusAccepted(
		resp.StatusCode,
		http.StatusNoContent,
		http.StatusOK,
		http.StatusNotFound,
	) {
		return kratosResponseError(resp)
	}

	return nil
}
