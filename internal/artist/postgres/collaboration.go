package postgres

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (s *store) ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error) {
	return auth.ResolveAuthenticatedPrincipalBySessionID(ctx, s.tx, sessionID)
}

func (s *store) RequireCurrentContributors(ctx context.Context, artistID string, contributors []string) error {
	return s.deps.Checkpoints.RequireCurrentContributors(ctx, s.tx, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_ARTIST, artistID, contributors)
}

func (s *store) RequireDocumentContributors(ctx context.Context, contributors []string) error {
	return s.deps.Translation.RequireDocumentContributors(ctx, s.tx, contributors)
}

func (s *store) ResolveInitialSourceLocale(ctx context.Context, acceptLanguage string) string {
	return s.deps.Translation.ResolveInitialSourceLocale(ctx, s.tx, acceptLanguage)
}

func (s *store) AppendMemberAudit(ctx context.Context, writer domainaudit.Appender, memberID string, action sharedtelemetry.AuditAction, build domainaudit.Builder) error {
	return domainaudit.AppendMember(ctx, s.tx, writer, memberID, action, build)
}
