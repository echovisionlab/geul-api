//go:build integration

package account

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

type adminAuthIdentityManager struct {
	identity                     *auth.Identity
	credentialScoped             bool
	includedCredentialTypes      []string
	deletedCredentialIdentifiers []string
	deletedSessions              []string
}

func (m *adminAuthIdentityManager) GetIdentity(_ context.Context, identityID string) (*auth.Identity, error) {
	if m.identity == nil || m.identity.ID != identityID {
		return nil, errors.New("account auth identity fixture missing")
	}
	return m.identity, nil
}

func (m *adminAuthIdentityManager) GetIdentityWithIncludeCredential(
	ctx context.Context,
	identityID string,
	credentialType string,
) (*auth.Identity, error) {
	m.includedCredentialTypes = append(m.includedCredentialTypes, credentialType)
	identity, err := m.GetIdentity(ctx, identityID)
	if err != nil || !m.credentialScoped {
		return identity, err
	}
	scoped := *identity
	scoped.Credentials = map[string]auth.Credential{}
	if credential, ok := identity.Credentials[credentialType]; ok {
		scoped.Credentials[credentialType] = credential
	}
	return &scoped, nil
}

func (m *adminAuthIdentityManager) ListIdentities(context.Context, int, int) ([]*auth.Identity, int64, error) {
	return nil, 0, nil
}
func (m *adminAuthIdentityManager) UpdateIdentityTraits(context.Context, string, structured.Fields) error {
	return nil
}
func (m *adminAuthIdentityManager) UpdateIdentityVerifiableAddresses(context.Context, string, []auth.VerifiableAddress) error {
	return nil
}
func (m *adminAuthIdentityManager) UpdateIdentityAccountEmailState(_ context.Context, identityID string, currentEmail *string, traits structured.Fields, addresses []auth.VerifiableAddress) error {
	if m.identity == nil || m.identity.ID != identityID {
		return errors.New("account auth identity fixture missing")
	}
	if m.identity.Traits == nil {
		m.identity.Traits = structured.Fields{}
	}
	if currentEmail != nil {
		email := strings.ToLower(strings.TrimSpace(*currentEmail))
		m.identity.Traits["email"] = email
		m.identity.Credentials["code"] = auth.Credential{Type: "code", Identifiers: []string{email}}
	}
	for key, value := range traits {
		if value == nil {
			delete(m.identity.Traits, key)
		} else {
			m.identity.Traits[key] = value
		}
	}
	if addresses != nil {
		m.identity.VerifiableAddresses = addresses
	}
	return nil
}
func (m *adminAuthIdentityManager) UpdateIdentityMetadataAdmin(context.Context, string, structured.Fields) error {
	return nil
}
func (m *adminAuthIdentityManager) SetIdentityState(context.Context, string, string) error {
	return nil
}
func (m *adminAuthIdentityManager) DeleteIdentitySessions(_ context.Context, identityID string) error {
	m.deletedSessions = append(m.deletedSessions, identityID)
	return nil
}
func (m *adminAuthIdentityManager) DeleteSession(_ context.Context, sessionID string) error {
	m.deletedSessions = append(m.deletedSessions, "session:"+sessionID)
	return nil
}
func (m *adminAuthIdentityManager) DeleteIdentity(context.Context, string) error { return nil }
func (m *adminAuthIdentityManager) GetIdentityEmail(context.Context, string) (string, error) {
	if m.identity == nil {
		return "", nil
	}
	return m.identity.CurrentEmail(), nil
}

func (m *adminAuthIdentityManager) DeleteIdentityCredentialIdentifier(
	_ context.Context,
	identityID, credentialType, identifier string,
) error {
	if m.identity == nil || m.identity.ID != identityID {
		return errors.New("account auth identity fixture missing")
	}
	m.deletedCredentialIdentifiers = append(
		m.deletedCredentialIdentifiers,
		identityID+":"+credentialType+":"+identifier,
	)
	credential, ok := m.identity.Credentials[credentialType]
	if !ok {
		return nil
	}
	provider, subject, ok := strings.Cut(identifier, ":")
	if !ok {
		return nil
	}
	nextIdentifiers := make([]string, 0, len(credential.Identifiers))
	for _, existing := range credential.Identifiers {
		existingProvider, existingSubject, found := strings.Cut(strings.TrimSpace(existing), ":")
		if !found || !strings.EqualFold(existingProvider, provider) || existingSubject != subject {
			nextIdentifiers = append(nextIdentifiers, existing)
		}
	}
	credential.Identifiers = nextIdentifiers
	if len(nextIdentifiers) == 0 {
		delete(m.identity.Credentials, credentialType)
	} else {
		m.identity.Credentials[credentialType] = credential
	}
	return nil
}
