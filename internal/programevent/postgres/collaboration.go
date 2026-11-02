package programeventpostgres

import (
	"context"

	translationpostgres "github.com/echovisionlab/geul-api/internal/translation/postgres"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

func (s *store) ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error) {
	return auth.ResolveAuthenticatedPrincipalBySessionID(ctx, s.tx, sessionID)
}

func (s *store) LockSession(ctx context.Context, sessionID string, principal *auth.UserInfo) error {
	return auth.LockAuthenticatedSessionForPrincipal(ctx, s.tx, sessionID, principal)
}

func (s *store) LockActivePrincipal(ctx context.Context, principal *auth.UserInfo) (bool, error) {
	return identitystate.LockActivePrincipal(ctx, s.tx, principal)
}

func (s *store) RequireCurrentContributors(ctx context.Context, eventID string, contributors []string) error {
	return s.ports.Checkpoints.RequireCurrentContributors(
		ctx,
		s.tx,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PROGRAM_EVENT,
		eventID,
		contributors,
	)
}

func (s *store) LockMembers(ctx context.Context, memberIDs []string) (int, error) {
	return s.ports.Members.LockMembers(ctx, s.tx, memberIDs)
}

func (s *store) PreferredLocale(ctx context.Context, memberID string) (*string, error) {
	return s.ports.Members.PreferredLocale(ctx, s.tx, memberID)
}

func (s *store) TranslationSettings(ctx context.Context) (translation.RuntimeSettings, error) {
	return translationpostgres.NewRuntimeSettingsReader(s.tx).RuntimeSettings(ctx)
}

func (s *store) PublishContentUpdate(ctx context.Context, update programevent.ContentUpdate) error {
	return contentsignal.PublishInTransaction(ctx, s.tx, s.ports.Signals, contentUpdatedEvent(update))
}
