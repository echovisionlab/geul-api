// Package hydra is the Hydra Admin API client of the OAuth app use cases.
// Only trusted in-cluster callers reach the Admin API; it has no
// authentication of its own.
package hydra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/oauthapp"
)

const (
	requestTimeout = 10 * time.Second
	pageSize       = 500
	// maxPages bounds a client listing; Hydra holds a few dozen clients.
	maxPages = 20
)

// Clients reads and writes OAuth clients through the Hydra Admin API.
type Clients struct {
	adminURL *url.URL
	http     *http.Client
}

var _ oauthapp.Clients = (*Clients)(nil)

// NewClients returns the Admin API client of adminURL, such as
// http://hydra:4445.
func NewClients(adminURL string, client *http.Client) (*Clients, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(adminURL), "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("hydra admin URL %q must be an absolute http(s) URL", adminURL)
	}
	if client == nil {
		client = &http.Client{}
	}
	adminClient := *client
	adminClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Clients{adminURL: parsed, http: &adminClient}, nil
}

type clientBody struct {
	ClientID                string          `json:"client_id"`
	ClientName              string          `json:"client_name"`
	ClientSecret            string          `json:"client_secret,omitempty"`
	RedirectURIs            []string        `json:"redirect_uris"`
	PostLogoutRedirectURIs  []string        `json:"post_logout_redirect_uris"`
	GrantTypes              []string        `json:"grant_types"`
	ResponseTypes           []string        `json:"response_types"`
	Scope                   string          `json:"scope"`
	Audience                []string        `json:"audience"`
	TokenEndpointAuthMethod string          `json:"token_endpoint_auth_method"`
	AccessTokenLifespan     string          `json:"authorization_code_grant_access_token_lifespan,omitempty"`
	IDTokenLifespan         string          `json:"authorization_code_grant_id_token_lifespan,omitempty"`
	Metadata                json.RawMessage `json:"metadata,omitempty"`
	CreatedAt               *time.Time      `json:"created_at,omitempty"`
}

type metadataMarker struct {
	Kind string `json:"kind"`
}

func (c *Clients) List(ctx context.Context) ([]oauthapp.Client, error) {
	next := c.endpoint("/admin/clients")
	query := next.Query()
	query.Set("page_size", fmt.Sprint(pageSize))
	next.RawQuery = query.Encode()
	var clients []oauthapp.Client
	for page := 0; next != nil; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("hydra client listing exceeds %d pages", maxPages)
		}
		var bodies []clientBody
		header, err := c.send(ctx, http.MethodGet, next.String(), nil, &bodies)
		if err != nil {
			return nil, err
		}
		for _, body := range bodies {
			clients = append(clients, clientOf(body))
		}
		next, err = c.nextPage(header.Get("Link"))
		if err != nil {
			return nil, err
		}
	}
	return clients, nil
}

func (c *Clients) Get(ctx context.Context, clientID string) (oauthapp.Client, error) {
	var body clientBody
	if _, err := c.send(ctx, http.MethodGet, c.clientURL(clientID), nil, &body); err != nil {
		return oauthapp.Client{}, err
	}
	return clientOf(body), nil
}

func (c *Clients) Create(ctx context.Context, client oauthapp.Client) error {
	body, err := bodyOf(client)
	if err != nil {
		return err
	}
	_, err = c.send(ctx, http.MethodPost, c.endpoint("/admin/clients").String(), body, nil)
	return err
}

func (c *Clients) Replace(ctx context.Context, client oauthapp.Client) error {
	body, err := bodyOf(client)
	if err != nil {
		return err
	}
	_, err = c.send(ctx, http.MethodPut, c.clientURL(client.ID), body, nil)
	return err
}

func (c *Clients) Delete(ctx context.Context, clientID string) error {
	_, err := c.send(ctx, http.MethodDelete, c.clientURL(clientID), nil, nil)
	return err
}

func (c *Clients) endpoint(path string) *url.URL {
	endpoint := *c.adminURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	return &endpoint
}

// clientURL escapes clientID as one path segment; connected AI tools use
// URL-form IDs.
func (c *Clients) clientURL(clientID string) string {
	endpoint := c.endpoint("/admin/clients/" + clientID)
	endpoint.RawPath = strings.TrimRight(c.adminURL.EscapedPath(), "/") + "/admin/clients/" + url.PathEscape(clientID)
	return endpoint.String()
}

var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// nextPage follows Hydra's Link rel="next", which is relative to the Admin
// API origin.
func (c *Clients) nextPage(link string) (*url.URL, error) {
	match := nextLink.FindStringSubmatch(link)
	if match == nil {
		return nil, nil
	}
	reference, err := url.Parse(match[1])
	if err != nil {
		return nil, fmt.Errorf("hydra returned an invalid next page link: %w", err)
	}
	next := c.adminURL.ResolveReference(reference)
	if next.Scheme != c.adminURL.Scheme || next.Host != c.adminURL.Host {
		return nil, errors.New("hydra returned a next page link to another origin")
	}
	return next, nil
}

// send returns the response headers; it closes the body itself.
func (c *Clients) send(ctx context.Context, method, endpoint string, body, result any) (http.Header, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode Hydra client: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("hydra %s %s: %w", method, request.URL.Path, err)
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusNotFound:
		return nil, oauthapp.ErrClientNotFound
	case response.StatusCode == http.StatusConflict:
		return nil, oauthapp.ErrClientExists
	case response.StatusCode < 200 || response.StatusCode > 299:
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return nil, fmt.Errorf("hydra %s %s returned %d: %s", method, request.URL.Path, response.StatusCode, strings.TrimSpace(string(detail)))
	}
	if result != nil {
		if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(result); err != nil {
			return nil, fmt.Errorf("decode Hydra %s %s: %w", method, request.URL.Path, err)
		}
	}
	return response.Header, nil
}

func bodyOf(client oauthapp.Client) (clientBody, error) {
	body := clientBody{
		ClientID: client.ID, ClientName: client.Name, ClientSecret: client.Secret,
		RedirectURIs: nonNil(client.RedirectURIs), PostLogoutRedirectURIs: nonNil(client.PostLogoutRedirectURIs),
		GrantTypes: nonNil(client.GrantTypes), ResponseTypes: nonNil(client.ResponseTypes), Scope: client.Scope,
		Audience: nonNil(client.Audience), TokenEndpointAuthMethod: client.AuthMethod,
		AccessTokenLifespan: lifespan(client.AccessTokenLifespan), IDTokenLifespan: lifespan(client.IDTokenLifespan),
	}
	if client.Kind != "" {
		marker, err := json.Marshal(metadataMarker{Kind: client.Kind})
		if err != nil {
			return clientBody{}, fmt.Errorf("encode Hydra client marker: %w", err)
		}
		body.Metadata = marker
	}
	return body, nil
}

func clientOf(body clientBody) oauthapp.Client {
	var marker metadataMarker
	if len(body.Metadata) != 0 {
		_ = json.Unmarshal(body.Metadata, &marker)
	}
	accessTokenLifespan, _ := time.ParseDuration(body.AccessTokenLifespan)
	idTokenLifespan, _ := time.ParseDuration(body.IDTokenLifespan)
	return oauthapp.Client{
		ID: body.ClientID, Name: body.ClientName, RedirectURIs: body.RedirectURIs,
		PostLogoutRedirectURIs: body.PostLogoutRedirectURIs, GrantTypes: body.GrantTypes,
		ResponseTypes: body.ResponseTypes, Scope: body.Scope, Audience: body.Audience,
		AuthMethod: body.TokenEndpointAuthMethod, AccessTokenLifespan: accessTokenLifespan,
		IDTokenLifespan: idTokenLifespan, Kind: marker.Kind, CreatedAt: body.CreatedAt,
	}
}

func lifespan(value time.Duration) string {
	if value <= 0 {
		return ""
	}
	return value.String()
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
