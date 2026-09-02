package auth

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/structured"
)

// OIDCProviderClaims contains only provider profile fields that authentication
// and account policies consume. Raw Kratos provider config and ID tokens are
// decoded once by CredentialInventory and do not leak into service layers.
type OIDCProviderClaims struct {
	Email                string
	PrimaryVerifiedEmail string
	Name                 string
	GivenName            string
	FamilyName           string
	Login                string
	PreferredUsername    string
	Nickname             string
	EmailVerified        bool
	EmailVerifiedPresent bool
	Verified             bool
	VerifiedPresent      bool
	Primary              bool
	PrimaryPresent       bool
}

// OIDCProviderCredential is the canonical OIDC provider entry returned by the
// Kratos Admin API when include_credential=oidc is requested.
type OIDCProviderCredential struct {
	Provider           string
	Subject            string
	InitialAccessToken string
	ClaimSets          []OIDCProviderClaims
}

// VerifiedAccountEmail returns only provider assertions that are strong enough
// to become an account-email candidate. Provider-specific claim interpretation
// belongs here so hooks, projections, and account reads cannot drift.
func (p OIDCProviderCredential) VerifiedAccountEmail() string {
	for _, claims := range p.ClaimSets {
		email := strings.TrimSpace(claims.Email)
		switch p.Provider {
		case "github":
			if primaryVerified := strings.TrimSpace(claims.PrimaryVerifiedEmail); primaryVerified != "" {
				email = primaryVerified
			} else if !claims.PrimaryPresent || !claims.Primary || !claims.VerifiedPresent || !claims.Verified {
				continue
			}
		case "google", "apple":
			if !claims.EmailVerifiedPresent || !claims.EmailVerified {
				continue
			}
		default:
			return ""
		}
		if ValidRealAccountEmail(email) {
			return email
		}
	}
	return ""
}

// NicknameCandidates returns non-authoritative provider-display candidates in
// the provider-specific order. Callers must still validate each value against
// the Member nickname contract before returning it to a client.
//
// Only the explicit generic OIDC provider is allowed to use the generic
// profile field order. Unknown providers intentionally return no suggestion.
func (p OIDCProviderCredential) NicknameCandidates() []string {
	var fields [][]string
	switch p.Provider {
	case "google":
		fields = [][]string{
			oidcClaimField(p.ClaimSets, func(claims OIDCProviderClaims) string { return claims.Name }),
			oidcClaimFullName(p.ClaimSets),
		}
	case "github":
		fields = [][]string{
			oidcClaimField(p.ClaimSets, func(claims OIDCProviderClaims) string { return claims.Name }),
			oidcClaimField(p.ClaimSets, func(claims OIDCProviderClaims) string { return claims.Login }),
			oidcClaimField(p.ClaimSets, func(claims OIDCProviderClaims) string { return claims.PreferredUsername }),
		}
	case "generic":
		fields = [][]string{
			oidcClaimField(p.ClaimSets, func(claims OIDCProviderClaims) string { return claims.Name }),
			oidcClaimField(p.ClaimSets, func(claims OIDCProviderClaims) string { return claims.PreferredUsername }),
			oidcClaimField(p.ClaimSets, func(claims OIDCProviderClaims) string { return claims.Nickname }),
			oidcClaimFullName(p.ClaimSets),
		}
	default:
		return nil
	}

	seen := make(map[string]struct{})
	var candidates []string
	for _, values := range fields {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			candidates = append(candidates, value)
		}
	}
	return candidates
}

func oidcClaimField(claimSets []OIDCProviderClaims, field func(OIDCProviderClaims) string) []string {
	values := make([]string, 0, len(claimSets))
	for _, claims := range claimSets {
		values = append(values, field(claims))
	}
	return values
}

func oidcClaimFullName(claimSets []OIDCProviderClaims) []string {
	values := make([]string, 0, len(claimSets))
	for _, claims := range claimSets {
		values = append(values, strings.TrimSpace(strings.TrimSpace(claims.GivenName)+" "+strings.TrimSpace(claims.FamilyName)))
	}
	return values
}

func ValidRealAccountEmail(email string) bool {
	normalized := strings.ToLower(strings.TrimSpace(email))
	return normalized != "" && strings.Contains(normalized, "@") && !strings.HasSuffix(normalized, ".local")
}

func (p OIDCProviderCredential) key() string {
	return oidcProviderKey(p.Provider, p.Subject)
}

// CredentialInventory gives every authentication guard and account projection
// the same interpretation of a Kratos identity's credentials.
type CredentialInventory struct {
	credentials map[string]Credential
	providers   []OIDCProviderCredential
}

func NewCredentialInventory(credentials map[string]Credential) CredentialInventory {
	inventory := CredentialInventory{credentials: credentials}
	credential, ok := credentials["oidc"]
	if !ok || (credential.Type != "" && credential.Type != "oidc") {
		return inventory
	}

	seen := make(map[string]struct{})
	if rawProviders, ok := credential.Config["providers"].(structured.Values); ok {
		for _, rawProvider := range rawProviders {
			config, ok := rawProvider.(structured.Fields)
			if !ok {
				continue
			}
			provider := OIDCProviderCredential{
				Provider:           NormalizeOIDCProvider(credentialString(config["provider"])),
				Subject:            strings.TrimSpace(credentialString(config["subject"])),
				InitialAccessToken: strings.TrimSpace(credentialString(config["initial_access_token"])),
				ClaimSets:          oidcProviderClaimSets(config),
			}
			key := provider.key()
			if key == "" {
				continue
			}
			seen[key] = struct{}{}
			inventory.providers = append(inventory.providers, provider)
		}
	}

	// Kratos exposes identifiers even when confidential credential config was
	// not requested. Preserve those providers without inventing token/config
	// data so last-method guards remain correct.
	for _, identifier := range credential.Identifiers {
		provider, subject, ok := strings.Cut(strings.TrimSpace(identifier), ":")
		entry := OIDCProviderCredential{
			Provider: NormalizeOIDCProvider(provider),
			Subject:  strings.TrimSpace(subject),
		}
		key := entry.key()
		if !ok || key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		inventory.providers = append(inventory.providers, entry)
	}
	return inventory
}

func (i CredentialInventory) OIDCProviders() []OIDCProviderCredential {
	return append([]OIDCProviderCredential(nil), i.providers...)
}

func (i CredentialInventory) HasOIDCProvider(provider, subject string) bool {
	key := oidcProviderKey(provider, subject)
	if key == "" {
		return false
	}
	for _, candidate := range i.providers {
		if candidate.key() == key {
			return true
		}
	}
	return false
}

// RecoverableAuthenticationMethodCount counts methods that can recover an
// account without a passkey: OIDC providers and the canonical email-code
// credential. Passkeys are deliberately excluded from this policy count.
func (i CredentialInventory) RecoverableAuthenticationMethodCount() int {
	count := len(i.providers)
	if credential, ok := i.credentials["code"]; ok && HasUsableCodeCredential(credential) {
		count++
	}
	return count
}

func (i CredentialInventory) HasRecoverableAuthenticationMethod() bool {
	return i.RecoverableAuthenticationMethodCount() > 0
}

func (i CredentialInventory) RecoverableAuthenticationMethodCountAfterOIDCRemoval(provider, subject string) int {
	count := i.RecoverableAuthenticationMethodCount()
	if i.HasOIDCProvider(provider, subject) {
		count--
	}
	return count
}

func oidcProviderKey(provider, subject string) string {
	provider = NormalizeOIDCProvider(provider)
	subject = strings.TrimSpace(subject)
	if provider == "" || subject == "" {
		return ""
	}
	return provider + ":" + subject
}

func NormalizeOIDCProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func CanonicalOIDCProviderIdentifier(provider, subject string) string {
	return oidcProviderKey(provider, subject)
}

// HasUsableCodeCredential reports whether Kratos has at least one address that
// can receive a passwordless login code. Admin API responses normally expose
// identifiers even when the confidential credential config is omitted.
func HasUsableCodeCredential(credential Credential) bool {
	if credential.Type != "" && credential.Type != "code" {
		return false
	}
	return len(rawCodeCredentialAddresses(credential)) > 0
}

// CodeCredentialHasAddress reports whether Kratos explicitly allows the email
// address to receive a login code for this identity. A successful code login
// verifies the address, so this intentionally does not depend on the identity's
// pre-login verifiable-address state.
func CodeCredentialHasAddress(credential Credential, email string) bool {
	if credential.Type != "" && credential.Type != "code" {
		return false
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return false
	}
	for _, address := range rawCodeCredentialAddresses(credential) {
		if strings.EqualFold(address, email) {
			return true
		}
	}
	return false
}

// CodeCredentialEmails returns the exact email identifiers that Kratos allows
// the code credential to use. A code credential is an authentication proof for
// each returned address, even when the same address is not present as a
// verified Kratos verifiable address.
func CodeCredentialEmails(credential Credential) []string {
	if credential.Type != "" && credential.Type != "code" {
		return nil
	}
	result := make([]string, 0, len(credential.Identifiers))
	for _, address := range rawCodeCredentialAddresses(credential) {
		if ValidRealAccountEmail(address) {
			result = append(result, address)
		}
	}
	return result
}

func rawCodeCredentialAddresses(credential Credential) []string {
	if credential.Type != "" && credential.Type != "code" {
		return nil
	}
	seen := make(map[string]struct{})
	result := make([]string, 0, len(credential.Identifiers))
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		normalized := strings.ToLower(value)
		if _, exists := seen[normalized]; exists {
			return
		}
		seen[normalized] = struct{}{}
		result = append(result, value)
	}
	for _, identifier := range credential.Identifiers {
		add(identifier)
	}
	if addresses, ok := credential.Config["addresses"].(structured.Values); ok {
		for _, rawAddress := range addresses {
			if address, ok := rawAddress.(structured.Fields); ok {
				add(credentialString(address["address"]))
			}
		}
	}
	return result
}

// UsablePasskeyCredentialCount counts concrete passkeys returned by Kratos
// with include_credential=passkey. Identifier-only credential shells do not
// contain enough information to report an account's actual passkey count.
func UsablePasskeyCredentialCount(credential Credential) int {
	return len(PasskeyCredentialIDs(credential))
}

// PasskeyCredentialIDs returns the canonical concrete passkey IDs from a
// confidential Kratos credential snapshot. Identifier-only shells are not
// exact enough for account mutation Audit.
func PasskeyCredentialIDs(credential Credential) []string {
	if credential.Type != "" && credential.Type != "passkey" {
		return nil
	}
	credentials, ok := credential.Config["credentials"].(structured.Values)
	if !ok {
		return nil
	}
	seen := make(map[string]struct{}, len(credentials))
	ids := make([]string, 0, len(credentials))
	for _, rawCredential := range credentials {
		passkey, ok := rawCredential.(structured.Fields)
		if !ok {
			continue
		}
		id := strings.TrimSpace(credentialString(passkey["id"]))
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func credentialString(value structured.Value) string {
	valueString, _ := value.(string)
	return valueString
}

func oidcProviderClaimSets(config structured.Fields) []OIDCProviderClaims {
	claimMaps := make([]structured.Fields, 0, 5)
	claimMaps = append(claimMaps, config)
	for _, key := range []string{"claims", "raw_claims", "profile"} {
		if claims, ok := config[key].(structured.Fields); ok {
			claimMaps = append(claimMaps, claims)
		}
	}
	if claims := decodeOIDCIDTokenClaims(credentialString(config["initial_id_token"])); claims != nil {
		claimMaps = append(claimMaps, claims)
	}

	result := make([]OIDCProviderClaims, 0, len(claimMaps))
	for _, claims := range claimMaps {
		parsed := OIDCProviderClaims{
			Email:                credentialString(claims["email"]),
			PrimaryVerifiedEmail: credentialString(claims["primary_verified_email"]),
			Name:                 credentialString(claims["name"]),
			GivenName:            credentialString(claims["given_name"]),
			FamilyName:           credentialString(claims["family_name"]),
			Login:                credentialString(claims["login"]),
			PreferredUsername:    credentialString(claims["preferred_username"]),
			Nickname:             credentialString(claims["nickname"]),
		}
		if verified, ok := claims["email_verified"]; ok {
			parsed.EmailVerifiedPresent = true
			parsed.EmailVerified = credentialBool(verified)
		}
		if verified, ok := claims["verified"]; ok {
			parsed.VerifiedPresent = true
			parsed.Verified = credentialBool(verified)
		}
		if primary, ok := claims["primary"]; ok {
			parsed.PrimaryPresent = true
			parsed.Primary = credentialBool(primary)
		}
		if parsed.Email != "" || parsed.PrimaryVerifiedEmail != "" || parsed.Name != "" || parsed.GivenName != "" ||
			parsed.FamilyName != "" || parsed.Login != "" || parsed.PreferredUsername != "" || parsed.Nickname != "" || parsed.EmailVerifiedPresent ||
			parsed.VerifiedPresent || parsed.PrimaryPresent {
			result = append(result, parsed)
		}
	}
	return result
}

func decodeOIDCIDTokenClaims(idToken string) structured.Fields {
	parts := strings.Split(strings.TrimSpace(idToken), ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims structured.Fields
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}

func credentialBool(value structured.Value) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}
