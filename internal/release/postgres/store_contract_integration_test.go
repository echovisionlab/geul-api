//go:build integration

package releasepostgres_test

import (
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/release"
	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
	"github.com/echovisionlab/geul-api/internal/release/releasetest"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestPostgresStoreKeepsTheReleaseStoreContractIntegration(t *testing.T) {
	t.Parallel()
	releasetest.RunStoreContract(t, func(t *testing.T) releasetest.Harness {
		// The Kratos stub backs the credited Member lock (identitystate).
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
		// The contract reaches the Release tables and the summary readers;
		// content, media, signals and collaboration stay with their own tests.
		ports := releasepostgres.Ports{
			Artists: releaseadapter.NewArtistSummaries(db), Labels: releaseadapter.NewLabelSummaries(db),
			ShareLinks: sharelinkadapter.EntityLinks{Type: sharelink.EntityRelease},
		}
		units := mutation.Postgres(db, func(tx *gorm.DB) release.Store { return releasepostgres.NewStore(tx, ports) })
		seedDocument := func(t *testing.T) string {
			id := uuid.NewString()
			require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'compact')`, id).Error)
			return id
		}
		seedFile := func(t *testing.T) string {
			id := uuid.NewString()
			digest := sha256.Sum256([]byte(id))
			require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256) VALUES (?, ?, 'audio/wav', 1024, 'wav', ?)`, id, id, digest[:]).Error)
			return id
		}
		return releasetest.Harness{
			Harness:      mutationtest.PostgresHarness(db, units),
			SeedDocument: seedDocument,
			SeedArtist: func(t *testing.T) string {
				id := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO artist (id, source_locale, content_document_id) VALUES (?, 'en', ?)`, id, seedDocument(t)).Error)
				return id
			},
			SeedLabel: func(t *testing.T) string {
				id := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO label (id, source_locale, content_document_id) VALUES (?, 'en', ?)`, id, seedDocument(t)).Error)
				return id
			},
			SeedReference: func(t *testing.T, relation release.ReferenceRelation, name string) string {
				table := map[release.ReferenceRelation]string{
					release.ReferenceCategories: "category", release.ReferenceGenres: "genre", release.ReferenceStyles: "style",
				}[relation]
				id := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO `+table+` (id, name, slug) VALUES (?, ?, ?)`, id, name, "slug-"+id).Error)
				return id
			},
			SeedFormat: func(t *testing.T, name string) string {
				id := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO format (id, name, slug) VALUES (?, ?, ?)`, id, name, "s-"+id[:8]).Error)
				return id
			},
			SeedMember: func(t *testing.T, nickname string, preferredLocale *string) string {
				id := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO member (id, nickname, preferred_locale) VALUES (?, ?, ?)`, id, nickname, preferredLocale).Error)
				return id
			},
			SeedFile: seedFile,
			SeedRestrictedDownload: func(t *testing.T, trackID string) {
				fileID := seedFile(t)
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					segmentID := uuid.NewString()
					if err := tx.Exec(`UPDATE track SET audio_original_file_id = ?, download_access = 'restricted' WHERE id = ?`, fileID, trackID).Error; err != nil {
						return err
					}
					if err := tx.Exec(`INSERT INTO member_criteria (id, name, kind) VALUES (?, ?, 'MEMBER_CRITERIA_KIND_ALL_MEMBERS')`, segmentID, "Segment "+segmentID[:8]).Error; err != nil {
						return err
					}
					return tx.Exec(`INSERT INTO track_download_member_criteria (track_id, member_criteria_id) VALUES (?, ?)`, trackID, segmentID).Error
				}))
			},
			SeedUploadSession: func(t *testing.T, trackID string) {
				require.NoError(t, db.Exec(`INSERT INTO upload_session (upload_id, file_id, upload_type, entity_id, entity_type, requested_mime)
					VALUES (?, ?, 'UPLOAD_TYPE_TRACK_AUDIO', ?, 'TRANSCODE_ENTITY_TYPE_TRACK', 'audio/wav')`,
					uuid.NewString(), seedFile(t), trackID).Error)
			},
		}
	})
}
