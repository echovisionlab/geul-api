// Package oauthapp owns the apps that sign people in with their site account
// and the AI tools connected to the site's editing tools. Hydra keeps every
// OAuth client's protocol settings; this domain records who may sign in
// through each sign-in app and decides it for the signed-in Member.
package oauthapp

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// Access is who may sign in through a sign-in app. The values are stored.
type Access string

const (
	AccessNone           Access = "SIGN_IN_ACCESS_NONE"
	AccessAdmins         Access = "SIGN_IN_ACCESS_ADMINS"
	AccessMembers        Access = "SIGN_IN_ACCESS_MEMBERS"
	AccessMemberCriteria Access = "SIGN_IN_ACCESS_MEMBER_CRITERIA"
)

func (a Access) valid() bool {
	switch a {
	case AccessNone, AccessAdmins, AccessMembers, AccessMemberCriteria:
		return true
	default:
		return false
	}
}

// AuthMethod is how a sign-in app authenticates at the token endpoint, as
// Hydra names it.
type AuthMethod string

const (
	AuthMethodClientSecretBasic AuthMethod = "client_secret_basic"
	AuthMethodClientSecretPost  AuthMethod = "client_secret_post"
	// AuthMethodNone is a public app; Hydra requires PKCE for it.
	AuthMethodNone AuthMethod = "none"
)

func (m AuthMethod) valid() bool {
	return m == AuthMethodClientSecretBasic || m == AuthMethodClientSecretPost || m == AuthMethodNone
}

// usesSecret reports whether the app proves itself with a client secret.
func (m AuthMethod) usesSecret() bool {
	return m != AuthMethodNone
}

// Scope is profile data a sign-in app may receive besides openid.
type Scope string

const (
	ScopeEmail   Scope = "email"
	ScopeProfile Scope = "profile"
)

// Lifespans a sign-in app's tokens may have. Without a refresh token, the
// access token lifespan bounds how long a removed Member keeps access.
var lifespans = []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour}

const (
	maxNameLength          = 100
	maxRedirectURIs        = 10
	maxCriteriaPerApp      = 20
	maxRedirectURIByteSize = 2000
)

// clientIDPattern accepts IDs an Admin chooses. They cannot contain ':' or
// '/', so they never collide with the URL-form IDs of connected AI tools.
var clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,63}$`)

// Settings are what an Admin chooses for a sign-in app.
type Settings struct {
	Name                   string
	Access                 Access
	MemberCriteriaIDs      []string
	RedirectURIs           []string
	PostLogoutRedirectURIs []string
	Scopes                 []Scope
	AuthMethod             AuthMethod
	AccessTokenLifespan    time.Duration
	IDTokenLifespan        time.Duration
}

// CriteriaSummary is a Member criteria as a sign-in app shows it.
type CriteriaSummary struct {
	ID          string
	Name        string
	Description *string
	Kind        string
	// Eligible is whether the criteria is active and selects Members by tag
	// or filter, the kinds that may admit Members to an app.
	Eligible bool
}

// App is a sign-in app: its Hydra client settings and who may sign in.
type App struct {
	ClientID       string
	Settings       Settings
	MemberCriteria []CriteriaSummary
	Revision       string
	CreatedAt      time.Time
	UpdatedAt      *time.Time
}

// normalizeSettings validates settings and returns them trimmed, with sets
// sorted and unique.
func normalizeSettings(settings Settings) (Settings, error) {
	settings.Name = strings.TrimSpace(settings.Name)
	if settings.Name == "" {
		return Settings{}, domainerr.Required("settings.name")
	}
	if utf8.RuneCountInString(settings.Name) > maxNameLength {
		return Settings{}, domainerr.InvalidField("settings.name", fmt.Sprintf("must be at most %d characters", maxNameLength))
	}
	if !settings.Access.valid() {
		return Settings{}, domainerr.InvalidField("settings.access", "is required")
	}
	criteriaIDs, err := normalizeCriteriaIDs(settings.Access, settings.MemberCriteriaIDs)
	if err != nil {
		return Settings{}, err
	}
	settings.MemberCriteriaIDs = criteriaIDs
	if settings.RedirectURIs, err = normalizeRedirectURIs("settings.redirect_uris", settings.RedirectURIs, true); err != nil {
		return Settings{}, err
	}
	if settings.PostLogoutRedirectURIs, err = normalizeRedirectURIs("settings.post_logout_redirect_uris", settings.PostLogoutRedirectURIs, false); err != nil {
		return Settings{}, err
	}
	if settings.Scopes, err = normalizeScopes(settings.Scopes); err != nil {
		return Settings{}, err
	}
	if !settings.AuthMethod.valid() {
		return Settings{}, domainerr.InvalidField("settings.auth_method", "is required")
	}
	if !slices.Contains(lifespans, settings.AccessTokenLifespan) {
		return Settings{}, domainerr.InvalidField("settings.access_token_lifespan", "is required")
	}
	if !slices.Contains(lifespans, settings.IDTokenLifespan) {
		return Settings{}, domainerr.InvalidField("settings.id_token_lifespan", "is required")
	}
	return settings, nil
}

func normalizeCriteriaIDs(access Access, ids []string) ([]string, error) {
	const field = "settings.member_criteria_ids"
	if access != AccessMemberCriteria {
		if len(ids) != 0 {
			return nil, domainerr.InvalidField(field, "must be empty unless access is MEMBER_CRITERIA")
		}
		return nil, nil
	}
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		parsed, err := uuid.Parse(strings.TrimSpace(id))
		if err != nil {
			return nil, domainerr.InvalidField(field, "must contain Member criteria IDs")
		}
		result = append(result, parsed.String())
	}
	slices.Sort(result)
	result = slices.Compact(result)
	if len(result) == 0 {
		return nil, domainerr.InvalidField(field, "must name at least one Member criteria")
	}
	if len(result) > maxCriteriaPerApp {
		return nil, domainerr.InvalidField(field, fmt.Sprintf("must name at most %d Member criteria", maxCriteriaPerApp))
	}
	return result, nil
}

// normalizeRedirectURIs accepts absolute https URIs, and http only for a
// loopback host, without a fragment or credentials.
func normalizeRedirectURIs(field string, uris []string, required bool) ([]string, error) {
	result := make([]string, 0, len(uris))
	for _, raw := range uris {
		value := strings.TrimSpace(raw)
		if len(value) > maxRedirectURIByteSize {
			return nil, domainerr.InvalidField(field, "contains a URI that is too long")
		}
		parsed, err := url.Parse(value)
		if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Fragment != "" || parsed.User != nil {
			return nil, domainerr.InvalidField(field, fmt.Sprintf("%q must be an absolute URI without a fragment", value))
		}
		if parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopback(parsed.Hostname())) {
			return nil, domainerr.InvalidField(field, fmt.Sprintf("%q must use https", value))
		}
		result = append(result, value)
	}
	slices.Sort(result)
	result = slices.Compact(result)
	if required && len(result) == 0 {
		return nil, domainerr.InvalidField(field, "must contain at least one URI")
	}
	if len(result) > maxRedirectURIs {
		return nil, domainerr.InvalidField(field, fmt.Sprintf("must contain at most %d URIs", maxRedirectURIs))
	}
	return result, nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func normalizeScopes(scopes []Scope) ([]Scope, error) {
	result := make([]Scope, 0, len(scopes))
	for _, scope := range scopes {
		if scope != ScopeEmail && scope != ScopeProfile {
			return nil, domainerr.InvalidField("settings.scopes", fmt.Sprintf("unsupported scope %q", scope))
		}
		result = append(result, scope)
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

// normalizeClientID returns the chosen ID, or a generated one when empty.
func normalizeClientID(clientID string) (string, error) {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return uuid.NewString(), nil
	}
	if !clientIDPattern.MatchString(clientID) {
		return "", domainerr.InvalidField("client_id", "must be 3 to 64 letters, digits, '.', '_' or '-', starting with a letter or digit")
	}
	return clientID, nil
}

// changedFields names the audit fields that differ between two settings.
func changedFields(before, after Settings) []string {
	var fields []string
	add := func(changed bool, field string) {
		if changed {
			fields = append(fields, field)
		}
	}
	add(before.Name != after.Name, "name")
	add(before.Access != after.Access, "access")
	add(!slices.Equal(before.MemberCriteriaIDs, after.MemberCriteriaIDs), "member_criteria")
	add(!slices.Equal(before.RedirectURIs, after.RedirectURIs), "redirect_uris")
	add(!slices.Equal(before.PostLogoutRedirectURIs, after.PostLogoutRedirectURIs), "post_logout_redirect_uris")
	add(!slices.Equal(before.Scopes, after.Scopes), "scopes")
	add(before.AuthMethod != after.AuthMethod, "auth_method")
	add(before.AccessTokenLifespan != after.AccessTokenLifespan || before.IDTokenLifespan != after.IDTokenLifespan, "token_lifespans")
	return fields
}
