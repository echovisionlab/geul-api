package translationadapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/artist"
	artistpostgres "github.com/echovisionlab/geul-api/internal/artist/postgres"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func artistDomainRegistration(audit domainaudit.Appender) domainRegistration {
	return newConfiguredDomainPort(core.KindArtist, domainPortFunctions{
		loadSourceDocument: func(ctx context.Context, db *gorm.DB, store *contentblock.Store, artistID string) (*core.SourceDocument, error) {
			if store == nil {
				return nil, errors.New("artist translation content block store is not configured")
			}
			return artist.LoadTypedTranslationSourceDocument(ctx, artistJoined(db, store, nil), artistID)
		},
		buildExtractionPlan: func(job core.PlanTarget, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			if job.EntityType != string(core.KindArtist) {
				return nil, errors.New("typed Artist translation source is required")
			}
			return core.BuildRichTextExtractionPlan(&job, source, core.RichTextDocumentFields{})
		},
		requestLocaleOG:   localeAwareOG(core.KindArtist),
		sourceLocaleAudit: sourceLocaleAudit(sharedtelemetry.AuditArtistUpdated, sharedtelemetry.NewArtistSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, NULL::text AS title, NULL::text AS summary, content_html, content_text, NULL::jsonb AS content_json, updated_at, NULL::uuid AS og_asset_id FROM %s`, table)
		},
		requireInterchangeView: genericInterchangeView(
			core.KindArtist,
			translationCanSet{view: policyv1.Artist.View, edit: policyv1.Artist.Edit},
			"",
		),
		requireSourceLocaleEdit: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, artistID string) error {
			return artist.RequireLockedSourceLocaleEdit(ctx, artistJoined(tx, nil, nil), spiceDB, func(principal *auth.UserInfo) (bool, error) {
				return identitystate.LockActivePrincipal(ctx, tx, principal)
			}, artistID)
		},
		requireTranslationRead: requireTranslationEdit(policyv1.Artist.Edit),
	})
}

// artistJoined binds the Artist Store to the translation domain's database
// handle, with the content store it hands the delivery.
func artistJoined(db *gorm.DB, contents *contentblock.Store, audit domainaudit.Appender) artist.Joined {
	return artistpostgres.Bind(db, artistpostgres.Ports{ContentBlocks: contents}, audit)
}
