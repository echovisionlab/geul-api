//go:build integration

package mediaassetpostgres_test

import (
	"context"
	"testing"
	"time"

	audiencepostgres "github.com/echovisionlab/geul-api/internal/audience/postgres"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type segmentConfigs struct{}

func (segmentConfigs) LoadSegmentConfigs(ctx context.Context, db *gorm.DB, rows []*model.AudienceSegment) error {
	return audiencepostgres.LoadSegmentConfigs(ctx, db, rows)
}

func TestDownloadStoreRequiresExactActiveIdentityLinkIntegration(t *testing.T) {
	t.Parallel()
	for _, condition := range []string{"active", "banned", "inactive", "not onboarded", "deleted", "wrong external member", "wrong identity"} {
		t.Run(condition, func(t *testing.T) {
			t.Parallel()
			db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true, BootstrapKratosStub: true}).DB
			identity := uuid.NewString()
			member := testutil.SeedPostIntegrationIdentity(t, db, identity, "download")
			switch condition {
			case "banned":
				require.NoError(t, db.Exec(`UPDATE kratos.identities SET metadata_admin = '{"banned":true}'::jsonb WHERE id = ?::uuid`, identity).Error)
			case "inactive":
				require.NoError(t, db.Exec(`UPDATE kratos.identities SET state = 'inactive' WHERE id = ?::uuid`, identity).Error)
			case "not onboarded":
				require.NoError(t, db.Exec(`UPDATE member SET onboarded = false WHERE id = ?::uuid`, member).Error)
			case "deleted":
				require.NoError(t, db.Exec(`UPDATE member SET deleted_at = now(), account_identity_id = NULL, bio = NULL, website = NULL, social_links = '{}'::jsonb, preferred_locale = NULL, primary_email = NULL, available_emails = '{}'::text[] WHERE id = ?::uuid`, member).Error)
			case "wrong external member":
				require.NoError(t, db.Exec(`UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid`, uuid.NewString(), identity).Error)
			case "wrong identity":
				identity = uuid.NewString()
			}
			facts, found, err := mediaassetpostgres.BindDownload(db, segmentConfigs{}).MemberFacts(t.Context(), member, identity)
			require.NoError(t, err)
			require.Equal(t, condition == "active", found)
			if found {
				require.Equal(t, member, facts.MemberID)
				require.Equal(t, identity, facts.IdentityID)
				require.True(t, facts.Onboarded)
			}
		})
	}
}
func TestDownloadPolicyQueryBudgetIsIndependentOfSegmentCountIntegration(t *testing.T) {
	t.Parallel()
	one := downloadPolicyQueryCount(t, 1)
	many := downloadPolicyQueryCount(t, 12)
	require.Positive(t, one)
	require.Equal(t, one, many)
	require.LessOrEqual(t, many, 7)
}
func downloadPolicyQueryCount(t *testing.T, count int) int {
	t.Helper()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true, BootstrapKratosStub: true}).DB
	identity := uuid.NewString()
	member := testutil.SeedPostIntegrationIdentity(t, db, identity, "download")
	file, document, block, tag := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Create(&model.File{ID: file, FileName: "download", MimeType: "text/plain", FileSize: 1, Extension: "txt", SHA256: make([]byte, 32), CreatedAt: time.Now().UTC()}).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'post', ?::uuid)`, document, uuid.NewString()).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block (id, document_id, parent_block_id, container_slot, position, kind, shared_data) VALUES (?::uuid, ?::uuid, NULL, 'root', 0, 'file', '{}'::jsonb)`, block, document).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id, download_audience) VALUES (?::uuid, 'file', 'active', ?::uuid, 'restricted')`, block, file).Error)
	require.NoError(t, db.Exec(`INSERT INTO user_tag (id, name, created_at) VALUES (?::uuid, 'download', now())`, tag).Error)
	require.NoError(t, db.Exec(`INSERT INTO user_tag_mapping (member_id, tag_id, created_at) VALUES (?::uuid, ?::uuid, now())`, member, tag).Error)
	for range count {
		id := uuid.NewString()
		require.NoError(t, db.Create(&model.AudienceSegment{ID: id, Name: id, SegmentType: model.AudienceSegmentMemberTags, CreatedAt: time.Now().UTC()}).Error)
		require.NoError(t, db.Create(&model.AudienceSegmentUserTag{AudienceSegmentID: id, UserTagID: tag}).Error)
		require.NoError(t, db.Exec(`INSERT INTO content_block_attachment_download_audience_segment (block_id, reference_path, audience_segment_id) VALUES (?::uuid, 'file', ?::uuid)`, block, id).Error)
	}
	store := mediaassetpostgres.BindDownload(db, segmentConfigs{})
	sources, err := mediaasset.LoadContentBlockDownloadSources(t.Context(), store, []mediaasset.ContentBlockDownloadSelector{{BlockID: block, ReferencePath: "file"}})
	require.NoError(t, err)
	require.Len(t, sources, 1)
	queries := 0
	callback := "count_download_queries_" + uuid.NewString()
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callback, func(*gorm.DB) { queries++ }))
	defer func() { require.NoError(t, db.Callback().Query().Remove(callback)) }()
	allowed, err := mediaasset.EvaluateFileDownloadAccessBatch(t.Context(), store, nil, sources, &auth.UserInfo{IdentityID: auth.IdentityID(identity), MemberID: auth.MemberID(member), Authenticated: true, Onboarded: true})
	require.NoError(t, err)
	require.True(t, allowed[mediaasset.ContentBlockDownloadPolicyKey(block, "file")])
	return queries
}
