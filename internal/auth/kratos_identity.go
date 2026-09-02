package auth

import (
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
)

// Identity represents a Kratos identity.
type Identity struct {
	ID                  string                `json:"id"`
	ExternalID          string                `json:"external_id"`
	SchemaID            string                `json:"schema_id"`
	Traits              structured.Fields     `json:"traits"`
	Credentials         map[string]Credential `json:"credentials,omitempty"`
	VerifiableAddresses []VerifiableAddress   `json:"verifiable_addresses,omitempty"`
	MetadataPublic      structured.Fields     `json:"metadata_public"`
	MetadataAdmin       structured.Fields     `json:"metadata_admin,omitempty"`
	State               string                `json:"state"`
	CreatedAt           time.Time             `json:"created_at"`
	UpdatedAt           time.Time             `json:"updated_at"`
}

type VerifiableAddress struct {
	ID         string     `json:"id,omitempty"`
	Value      string     `json:"value"`
	Via        string     `json:"via"`
	Verified   bool       `json:"verified"`
	Status     string     `json:"status,omitempty"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
}

// Credential represents an identity credential entry from Kratos.
type Credential struct {
	Type        string            `json:"type"`
	Identifiers []string          `json:"identifiers"`
	Config      structured.Fields `json:"config,omitempty"`
}

// GetTraitString extracts a string trait value.
func (i *Identity) GetTraitString(key string) *string {
	if i == nil || i.Traits == nil {
		return nil
	}
	if v, ok := i.Traits[key].(string); ok && v != "" {
		return &v
	}
	return nil
}

func (i *Identity) CurrentEmail() string {
	if i == nil {
		return ""
	}
	if email := i.GetTraitString("email"); email != nil {
		return strings.TrimSpace(*email)
	}
	return ""
}

func (i *Identity) PendingEmail() string {
	if i == nil {
		return ""
	}
	if email := i.GetTraitString("pending_email"); email != nil {
		return strings.TrimSpace(*email)
	}
	return ""
}

func (i *Identity) CurrentEmailVerified() bool {
	if i == nil {
		return false
	}
	return i.HasVerifiedEmailAddress(i.CurrentEmail())
}

func (i *Identity) HasVerifiedEmailAddress(email string) bool {
	return i.hasEmailAddressWithVerification(email, true)
}

func (i *Identity) HasUnverifiedEmailAddress(email string) bool {
	return i.hasEmailAddressWithVerification(email, false)
}

func (i *Identity) hasEmailAddressWithVerification(email string, verified bool) bool {
	if i == nil {
		return false
	}
	normalizedEmail := normalizeIdentityEmail(email)
	if normalizedEmail == "" {
		return false
	}
	if strings.HasSuffix(normalizedEmail, ".local") {
		return false
	}
	for _, address := range i.VerifiableAddresses {
		if strings.EqualFold(strings.TrimSpace(address.Via), "email") &&
			address.Verified == verified &&
			normalizeIdentityEmail(address.Value) == normalizedEmail {
			return true
		}
	}
	return false
}

func normalizeIdentityEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// GetTraitMap extracts a map trait value.
func (i *Identity) GetTraitMap(key string) map[string]string {
	if i == nil || i.Traits == nil {
		return nil
	}
	if v, ok := i.Traits[key].(structured.Fields); ok {
		result := make(map[string]string)
		for k, val := range v {
			if s, ok := val.(string); ok {
				result[k] = s
			}
		}
		return result
	}
	return nil
}

// IsBanned checks if the identity is banned.
func (i *Identity) IsBanned() bool {
	if i == nil {
		return false
	}
	if i.State == KratosStateInactive {
		return true
	}
	if i.MetadataAdmin != nil {
		if banned, ok := i.MetadataAdmin["banned"].(bool); ok {
			return banned
		}
	}
	return false
}

// GetBanReason returns the ban reason if set.
func (i *Identity) GetBanReason() *string {
	if i == nil || i.MetadataAdmin == nil {
		return nil
	}
	if reason, ok := i.MetadataAdmin["ban_reason"].(string); ok && reason != "" {
		return &reason
	}
	return nil
}
