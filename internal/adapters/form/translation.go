package form

import (
	"context"

	collaborationadapter "github.com/echovisionlab/geul-api/internal/adapters/collaboration"
	"github.com/echovisionlab/geul-api/internal/auth"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"gorm.io/gorm"
)

type Translation struct{}

func NewTranslation() *Translation { return &Translation{} }

func (*Translation) ResolveInitialSourceLocale(
	ctx context.Context,
	db *gorm.DB,
	identity auth.IdentityManager,
	acceptLanguage string,
) string {
	return formdomain.ResolveInitialSourceLocale(ctx, db, identity, acceptLanguage)
}
func (*Translation) NormalizeInitialSourceLocale(ctx context.Context, db *gorm.DB, locale string) string {
	return formdomain.NormalizeInitialSourceLocale(ctx, db, locale)
}
func (*Translation) DefaultLocale(ctx context.Context, db *gorm.DB) string {
	return formdomain.TranslationDefaultLocale(ctx, db)
}
func (*Translation) LockRoot(ctx context.Context, db *gorm.DB, formID string) error {
	return formdomain.LockTranslationRoot(ctx, db, formID)
}
func (*Translation) RequireMutationContributor(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	kind intrav1.CollaborationResourceType,
	id string,
	contributor string,
) error {
	return collaborationadapter.NewCheckpointFence(tx, spiceDB).
		RequireCurrentContributors(ctx, tx, kind, id, []string{contributor})
}

var _ formdomain.Translation = (*Translation)(nil)
