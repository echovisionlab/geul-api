package oauthapp

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// ConnectedTool is an AI tool that registered itself to connect to the
// site's editing tools: a Hydra client whose tokens target a resource.
type ConnectedTool struct {
	ClientID string
	Name     string
	// Publisher is the host of a URL-form client ID; nil for a generated ID.
	Publisher *string
	// Stale is set when the tool targets a resource the site no longer
	// serves.
	Stale        bool
	RedirectURIs []string
	Resource     string
	AuthMethod   string
	CreatedAt    *time.Time
}

// ListConnectedTools returns the connected AI tools, ordered by name.
func (s *Service) ListConnectedTools(ctx context.Context) ([]ConnectedTool, error) {
	if err := s.requireAdmin(ctx, policyv1.OauthConnectedTool.List); err != nil {
		return nil, err
	}
	var records []Record
	if err := s.units.Read(ctx, func(store Store) (err error) {
		records, err = store.Apps(ctx)
		return err
	}); err != nil {
		return nil, err
	}
	clients, err := s.clients.List(ctx)
	if err != nil {
		return nil, hydraUnavailable(err)
	}
	tools := make([]ConnectedTool, 0, len(clients))
	for _, client := range clients {
		if isConnectedTool(client) && !slices.ContainsFunc(records, func(record Record) bool { return record.ClientID == client.ID }) {
			tools = append(tools, s.connectedTool(client))
		}
	}
	slices.SortFunc(tools, func(a, b ConnectedTool) int {
		if order := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); order != 0 {
			return order
		}
		return strings.Compare(a.ClientID, b.ClientID)
	})
	return tools, nil
}

// DisconnectConnectedTool deletes the tool's Hydra client; its tokens stop
// working, and it connects again only after a Member approves it again.
func (s *Service) DisconnectConnectedTool(ctx context.Context, clientID string) error {
	clientID = strings.TrimSpace(clientID)
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Access: mutation.FreshAdminCan(policyv1.OauthConnectedTool.Disconnect()),
		Change: func(ctx context.Context, store Store) (string, error) {
			if _, err := store.App(ctx, clientID); !errors.Is(err, ErrAppNotFound) {
				if err != nil {
					return "", err
				}
				return "", toolNotFound(clientID)
			}
			client, err := s.clients.Get(ctx, clientID)
			if errors.Is(err, ErrClientNotFound) || (err == nil && !isConnectedTool(client)) {
				return "", toolNotFound(clientID)
			}
			if err != nil {
				return "", hydraUnavailable(err)
			}
			if err := s.clients.Delete(ctx, clientID); err != nil && !errors.Is(err, ErrClientNotFound) {
				return "", hydraUnavailable(err)
			}
			return clientID, nil
		},
		Audit: func(clientID string) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditOAuthConnectedToolDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewOAuthConnectedToolDeletedAuditRecord(metadata, clientID)
			})
		},
	})
	return err
}

// isConnectedTool reports whether client is an AI tool connection rather
// than a sign-in app or an operator-managed client.
func isConnectedTool(client Client) bool {
	return client.Kind != signInAppKind && len(client.Audience) != 0
}

func (s *Service) connectedTool(client Client) ConnectedTool {
	tool := ConnectedTool{
		ClientID: client.ID, Name: strings.TrimSpace(client.Name), RedirectURIs: slices.Clone(client.RedirectURIs),
		Resource: client.Audience[0], AuthMethod: client.AuthMethod, CreatedAt: client.CreatedAt,
		Stale: !slices.Contains(client.Audience, s.mcpResourceURL),
	}
	if parsed, err := url.Parse(client.ID); err == nil && parsed.Scheme == "https" && parsed.Host != "" {
		host := parsed.Hostname()
		tool.Publisher = &host
	}
	return tool
}

func toolNotFound(clientID string) error {
	return domainerr.ResourceNotFound("connected AI tool", clientID)
}
