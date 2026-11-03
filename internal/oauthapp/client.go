package oauthapp

import (
	"slices"
	"strings"
)

// signInGrantTypes gives a sign-in app no refresh token, so every sign-in
// passes the login app and its access check again.
var signInGrantTypes = []string{"authorization_code"}

// clientOf is the Hydra client of a sign-in app with settings.
func clientOf(clientID string, settings Settings) Client {
	scope := []string{"openid"}
	for _, extra := range settings.Scopes {
		scope = append(scope, string(extra))
	}
	return Client{
		ID:                     clientID,
		Name:                   settings.Name,
		RedirectURIs:           slices.Clone(settings.RedirectURIs),
		PostLogoutRedirectURIs: slices.Clone(settings.PostLogoutRedirectURIs),
		GrantTypes:             slices.Clone(signInGrantTypes),
		ResponseTypes:          []string{"code"},
		Scope:                  strings.Join(scope, " "),
		AuthMethod:             string(settings.AuthMethod),
		AccessTokenLifespan:    settings.AccessTokenLifespan,
		IDTokenLifespan:        settings.IDTokenLifespan,
		Kind:                   signInAppKind,
	}
}

// settingsOf reads a sign-in app's settings back from its record and Hydra
// client.
func settingsOf(record Record, client Client) Settings {
	var scopes []Scope
	for _, value := range strings.Fields(client.Scope) {
		if scope := Scope(value); scope == ScopeEmail || scope == ScopeProfile {
			scopes = append(scopes, scope)
		}
	}
	slices.Sort(scopes)
	redirects := slices.Clone(client.RedirectURIs)
	slices.Sort(redirects)
	logoutRedirects := slices.Clone(client.PostLogoutRedirectURIs)
	slices.Sort(logoutRedirects)
	return Settings{
		Name:                   client.Name,
		Access:                 record.Access,
		MemberCriteriaIDs:      slices.Clone(record.MemberCriteriaIDs),
		RedirectURIs:           redirects,
		PostLogoutRedirectURIs: logoutRedirects,
		Scopes:                 scopes,
		AuthMethod:             AuthMethod(client.AuthMethod),
		AccessTokenLifespan:    client.AccessTokenLifespan,
		IDTokenLifespan:        client.IDTokenLifespan,
	}
}

// missingClientSettings shows an app whose Hydra client is gone under its
// client ID, so an Admin can still find and delete it.
func missingClientSettings(record Record) Settings {
	return Settings{
		Name: record.ClientID, Access: record.Access, MemberCriteriaIDs: slices.Clone(record.MemberCriteriaIDs),
	}
}

func appOf(record Record, settings Settings, criteria []CriteriaSummary) App {
	updatedAt := record.UpdatedAt
	return App{
		ClientID: record.ClientID, Settings: settings, MemberCriteria: criteria, Revision: record.Revision,
		CreatedAt: record.CreatedAt, UpdatedAt: &updatedAt,
	}
}
