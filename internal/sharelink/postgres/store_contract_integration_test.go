//go:build integration

package sharelinkpostgres_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	sharelinkpostgres "github.com/echovisionlab/geul-api/internal/sharelink/postgres"
	"github.com/echovisionlab/geul-api/internal/sharelink/sharelinktest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestPostgresStoreKeepsTheStoreContractIntegration(t *testing.T) {
	t.Parallel()
	sharelinktest.RunStoreContract(t, func(t *testing.T) sharelinktest.Harness {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		return sharelinktest.Harness{
			Harness: mutationtest.PostgresHarness(db, sharelinkpostgres.NewUnits(db)),
			SeedTarget: func(t *testing.T, entityType sharelink.EntityType, state sharelink.Target) string {
				return seedTarget(t, db, entityType, state)
			},
		}
	})
}

// seedTarget stores the smallest row of the target's table that its owner's
// constraints accept, in state.
func seedTarget(t *testing.T, db *gorm.DB, entityType sharelink.EntityType, state sharelink.Target) string {
	t.Helper()
	id, documentID := uuid.NewString(), uuid.NewString()
	profile := "compact"
	insert := func(statement string, values ...any) {
		require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, ?)`, documentID, profile).Error)
		require.NoError(t, db.Exec(statement, append([]any{id, documentID}, values...)...).Error)
	}
	status := func(archived, other string) string {
		if state.Archived {
			return archived
		}
		return other
	}
	switch entityType {
	case sharelink.EntityPost:
		profile = "post"
		insert(`INSERT INTO post (id, content_document_id, status) VALUES (?, ?, ?)`,
			status(string(model.PostStatusArchived), string(model.PostStatusDraft)))
	case sharelink.EntityWork:
		profile = "work"
		insert(`INSERT INTO work (id, content_document_id, type, year, month, is_present, status) VALUES (?, ?, 'WORK_TYPE_PORTFOLIO', 2026, 1, true, ?)`,
			status(string(model.WorkStatusArchived), string(model.WorkStatusDraft)))
	case sharelink.EntityPage:
		profile = "page"
		insert(`INSERT INTO page (id, content_document_id) VALUES (?, ?)`)
	case sharelink.EntityRelease:
		insert(`INSERT INTO release (id, content_document_id, type) VALUES (?, ?, 'RELEASE_TYPE_ALBUM')`)
	case sharelink.EntityForm, sharelink.EntityFormDashboard:
		insert(`INSERT INTO form (id, content_document_id) VALUES (?, ?)`)
	case sharelink.EntityLabel:
		insert(`INSERT INTO label (id, content_document_id) VALUES (?, ?)`)
	case sharelink.EntityArtist:
		insert(`INSERT INTO artist (id, content_document_id) VALUES (?, ?)`)
	case sharelink.EntityPrivacy, sharelink.EntityTerms:
		profile = "policy"
		table, scheduled, archived, draft := "privacy_history", model.PrivacyStatusScheduled, model.PrivacyStatusArchived, model.PrivacyStatusDraft
		if entityType == sharelink.EntityTerms {
			table, scheduled, archived, draft = "terms_history", model.TermsStatusScheduled, model.TermsStatusArchived, model.TermsStatusDraft
		}
		stored := status(string(archived), string(draft))
		if state.Scheduled {
			stored = string(scheduled)
		}
		insert(`INSERT INTO `+table+` (id, content_document_id, version, content, status, effective_from) VALUES (?, ?, ?, '', ?, ?)`,
			state.Version, stored, state.EffectiveFrom)
	default:
		t.Fatalf("no table for share link target type %q", entityType)
	}
	return id
}
