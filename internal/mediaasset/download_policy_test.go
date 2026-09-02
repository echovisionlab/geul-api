//go:build integration

package mediaasset_test

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/auth"
	. "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEvaluateFileDownloadAccessAudienceMatrixUnit(t *testing.T) {
	db := newFileDownloadPolicyDB(t)
	now := time.Now().UTC()
	fileID := uuid.NewString()
	userID := uuid.NewString()
	adminID := uuid.NewString()
	tagID := uuid.NewString()
	segmentID := uuid.NewString()
	seedDownloadIdentity(t, db, userID, policyv1.Role.User().ID(), now.Add(-time.Hour))
	seedDownloadIdentity(t, db, adminID, policyv1.Role.Admin().ID(), now.Add(-time.Hour))
	require.NoError(t, db.Exec(
		`INSERT INTO user_tag (id, name, created_at) VALUES (?, 'Members', ?)`,
		tagID,
		now,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO user_tag_mapping (member_id, tag_id, created_at) VALUES (?, ?, ?)`,
		userID,
		tagID,
		now,
	).Error)
	seedDownloadAudienceSegment(
		t,
		db,
		segmentID,
		managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS,
		model.AudienceSegmentConfig{MemberTagIDs: []string{tagID}},
	)
	require.NoError(t, db.Exec(
		`INSERT INTO track_download_audience_segment (
			track_id, audience_segment_id, created_at
		) VALUES (?, ?, ?)`,
		fileID,
		segmentID,
		now,
	).Error)

	tests := []struct {
		name     string
		audience FileDownloadAudience
		user     *auth.UserInfo
		want     bool
	}{
		{name: "disabled anonymous", audience: FileDownloadAudienceDisabled},
		{
			name:     "public anonymous",
			audience: FileDownloadAudiencePublic,
			want:     true,
		},
		{
			name:     "authenticated active user",
			audience: FileDownloadAudienceAuthenticated,
			user:     downloadUserInfo(userID),
			want:     true,
		},
		{
			name:     "restricted anonymous",
			audience: FileDownloadAudienceRestricted,
		},
		{
			name:     "restricted matching member",
			audience: FileDownloadAudienceRestricted,
			user:     downloadUserInfo(userID),
			want:     true,
		},
		{
			name:     "restricted admin has no implicit bypass",
			audience: FileDownloadAudienceRestricted,
			user:     downloadUserInfo(adminID),
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := evaluateSingleFileDownloadAccessAt(
				context.Background(),
				db,
				FileDownloadSource{FileID: fileID, Audience: testCase.audience},
				testCase.user,
				now,
			)
			require.NoError(t, err)
			require.Equal(t, testCase.want, got)
		})
	}

	require.NoError(t, db.Table("audience_segment").
		Where("id = ?", segmentID).
		Update("archived_at", now).Error)
	allowed, err := evaluateSingleFileDownloadAccessAt(
		context.Background(),
		db,
		FileDownloadSource{
			FileID:   fileID,
			Audience: FileDownloadAudienceRestricted,
		},
		downloadUserInfo(userID),
		now,
	)
	require.NoError(t, err)
	require.False(t, allowed)
	presence, err := RestrictedFileDownloadSegmentPresence(
		context.Background(),
		db,
		map[string]FileDownloadSource{fileID: trackDownloadSource(fileID, FileDownloadAudienceRestricted)},
	)
	require.NoError(t, err)
	require.False(t, presence[fileID])
}

func TestEvaluateRestrictedDownloadFailsClosedForInvalidAndStaleAudienceConfigUnit(
	t *testing.T,
) {
	db := newFileDownloadPolicyDB(t)
	now := time.Now().UTC()
	userID := uuid.NewString()
	seedDownloadIdentity(t, db, userID, policyv1.Role.User().ID(), now.Add(-time.Hour))
	user := downloadUserInfo(userID)

	testCases := []struct {
		name        string
		segmentType managev1.SegmentType
		config      model.AudienceSegmentConfig
	}{
		{
			name:        "stale user tag",
			segmentType: managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS,
			config: model.AudienceSegmentConfig{
				MemberTagIDs: []string{uuid.NewString()},
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fileID := uuid.NewString()
			segmentID := uuid.NewString()
			seedDownloadAudienceSegment(t, db, segmentID, testCase.segmentType, testCase.config)
			require.NoError(t, db.Exec(
				`INSERT INTO track_download_audience_segment (
					track_id, audience_segment_id, created_at
				) VALUES (?, ?, ?)`,
				fileID,
				segmentID,
				now,
			).Error)
			allowed, err := evaluateSingleFileDownloadAccessAt(
				context.Background(),
				db,
				FileDownloadSource{FileID: fileID, Audience: FileDownloadAudienceRestricted},
				user,
				now,
			)
			require.NoError(t, err)
			require.False(t, allowed)
		})
	}
}

func TestEvaluateRestrictedDownloadAppliesFilterAndExclusionUnit(t *testing.T) {
	db := newFileDownloadPolicyDB(t)
	now := time.Now().UTC()
	stack := testutil.SetupOryStack(t)
	author := stack.CreateUser(t, policyv1.Role.Author().ID())
	userID := author.MemberID
	seedDownloadIdentityWithIdentityID(t, db, userID, author.IdentityID, policyv1.Role.Author().ID(), now.Add(-time.Hour))
	fileID := uuid.NewString()
	segmentID := uuid.NewString()
	createdAfter := now.Add(-2 * time.Hour)
	createdBefore := now.Add(time.Hour)
	seedDownloadAudienceSegment(
		t,
		db,
		segmentID,
		managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER,
		model.AudienceSegmentConfig{
			AccountRoles:  []string{policyv1.Role.Author().ID()},
			CreatedAfter:  &createdAfter,
			CreatedBefore: &createdBefore,
		},
	)
	require.NoError(t, db.Exec(
		`INSERT INTO track_download_audience_segment (
			track_id, audience_segment_id, created_at
		) VALUES (?, ?, ?)`,
		fileID,
		segmentID,
		now,
	).Error)
	source := FileDownloadSource{FileID: fileID, Audience: FileDownloadAudienceRestricted}
	user := author.AuthUserInfo()

	allowed, err := evaluateSingleFileDownloadAccessBatchAt(
		context.Background(),
		db,
		stack.SpiceDBClient,
		source,
		user,
		now,
	)
	require.NoError(t, err)
	require.True(t, allowed)

	require.NoError(t, db.Exec(
		`INSERT INTO audience_segment_excluded_member (
			audience_segment_id, member_id
		) VALUES (?, ?)`,
		segmentID,
		userID,
	).Error)
	allowed, err = evaluateSingleFileDownloadAccessBatchAt(
		context.Background(),
		db,
		stack.SpiceDBClient,
		source,
		user,
		now,
	)
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestEvaluateRestrictedDownloadBatchQueryCountDoesNotScaleWithSegmentsUnit(
	t *testing.T,
) {
	oneSegmentQueries := evaluateRestrictedDownloadBatchQueryCount(t, 1)
	manySegmentQueries := evaluateRestrictedDownloadBatchQueryCount(t, 12)

	require.Positive(t, oneSegmentQueries)
	require.Equal(t, oneSegmentQueries, manySegmentQueries)
	require.LessOrEqual(t, manySegmentQueries, 7)
}

func evaluateRestrictedDownloadBatchQueryCount(t *testing.T, segmentCount int) int {
	t.Helper()
	db := newFileDownloadPolicyDB(t)
	now := time.Now().UTC()
	memberID := uuid.NewString()
	fileID := uuid.NewString()
	tagID := uuid.NewString()
	seedDownloadIdentity(t, db, memberID, policyv1.Role.User().ID(), now.Add(-time.Hour))
	require.NoError(t, db.Exec(
		`INSERT INTO user_tag (id, name, created_at) VALUES (?, 'Members', ?)`,
		tagID,
		now,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO user_tag_mapping (member_id, tag_id, created_at) VALUES (?, ?, ?)`,
		memberID,
		tagID,
		now,
	).Error)
	for range segmentCount {
		segmentID := uuid.NewString()
		seedDownloadAudienceSegment(
			t,
			db,
			segmentID,
			managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS,
			model.AudienceSegmentConfig{MemberTagIDs: []string{tagID}},
		)
		require.NoError(t, db.Exec(
			`INSERT INTO track_download_audience_segment (
				track_id, audience_segment_id, created_at
			) VALUES (?, ?, ?)`,
			fileID,
			segmentID,
			now,
		).Error)
	}

	queryCount := 0
	callbackName := "count_restricted_file_download_queries_" + uuid.NewString()
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(
		callbackName,
		func(*gorm.DB) { queryCount++ },
	))
	t.Cleanup(func() {
		require.NoError(t, db.Callback().Query().Remove(callbackName))
	})
	access, err := EvaluateFileDownloadAccessBatch(
		t.Context(),
		db,
		nil,
		map[string]FileDownloadSource{
			fileID: trackDownloadSource(fileID, FileDownloadAudienceRestricted),
		},
		downloadUserInfo(memberID),
		testSegmentConfigLoader{},
	)
	require.NoError(t, err)
	require.True(t, access[fileID])
	return queryCount
}

func newFileDownloadPolicyDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`ATTACH DATABASE ':memory:' AS kratos`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE file (
			id TEXT PRIMARY KEY,
			extension TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			file_size INTEGER NOT NULL,
			file_name TEXT,
			delete_requested_at DATETIME
		);
		CREATE TABLE audience_segment (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT,
			segment_type TEXT NOT NULL,
			created_after DATETIME,
			created_before DATETIME,
			archived_at DATETIME,
			created_at DATETIME NOT NULL,
			updated_at DATETIME
		);
		CREATE TABLE audience_segment_user_tag (
			audience_segment_id TEXT NOT NULL,
			user_tag_id TEXT NOT NULL,
			PRIMARY KEY (audience_segment_id, user_tag_id)
		);
		CREATE TABLE audience_segment_user_role (
			audience_segment_id TEXT NOT NULL,
			role TEXT NOT NULL,
			PRIMARY KEY (audience_segment_id, role)
		);
		CREATE TABLE audience_segment_excluded_member (
			audience_segment_id TEXT NOT NULL,
			member_id TEXT NOT NULL,
			PRIMARY KEY (audience_segment_id, member_id)
		);
		CREATE TABLE track_download_audience_segment (
			track_id TEXT NOT NULL,
			audience_segment_id TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			PRIMARY KEY (track_id, audience_segment_id)
		);
		CREATE TABLE user_tag (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			created_at DATETIME NOT NULL
		);
		CREATE TABLE user_tag_mapping (
			member_id TEXT NOT NULL,
			tag_id TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			PRIMARY KEY (member_id, tag_id)
		);
		CREATE TABLE member (
			id TEXT PRIMARY KEY,
			account_identity_id TEXT NOT NULL,
			onboarded INTEGER NOT NULL DEFAULT 1,
			deleted_at DATETIME,
			created_at DATETIME NOT NULL
		);
		CREATE TABLE kratos.identities (
			id TEXT PRIMARY KEY,
			external_id TEXT NOT NULL,
			state TEXT NOT NULL,
			traits TEXT NOT NULL,
			metadata_public TEXT NOT NULL,
			metadata_admin TEXT NOT NULL,
			created_at DATETIME NOT NULL
		)
	`).Error)
	return db
}

func seedDownloadIdentity(
	t *testing.T,
	db *gorm.DB,
	id string,
	role string,
	createdAt time.Time,
) {
	t.Helper()
	identityID := id
	require.NoError(t, db.Exec(
		`INSERT INTO member (id, account_identity_id, onboarded, created_at) VALUES (?, ?, 1, ?)`,
		id,
		identityID,
		createdAt,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO kratos.identities (
			id, external_id, state, traits, metadata_public, metadata_admin, created_at
		) VALUES (?, ?, 'active', ?, ?, '{"banned":false}', ?)`,
		identityID,
		id,
		`{}`,
		`{"role":"`+role+`"}`,
		createdAt,
	).Error)
}

func seedDownloadIdentityWithIdentityID(
	t *testing.T,
	db *gorm.DB,
	memberID string,
	identityID string,
	role string,
	createdAt time.Time,
) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO member (id, account_identity_id, onboarded, created_at) VALUES (?, ?, 1, ?)`,
		memberID, identityID, createdAt,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO kratos.identities (
			id, external_id, state, traits, metadata_public, metadata_admin, created_at
		) VALUES (?, ?, 'active', ?, ?, '{"banned":false}', ?)`,
		identityID, memberID, `{}`, `{"role":"`+role+`"}`, createdAt,
	).Error)
}

func seedDownloadAudienceSegment(
	t *testing.T,
	db *gorm.DB,
	id string,
	segmentType managev1.SegmentType,
	config model.AudienceSegmentConfig,
) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO audience_segment (
			id, name, segment_type,
			created_after, created_before, created_at, updated_at
		) VALUES (?, 'Access', ?, ?, ?, ?, ?)`,
		id,
		segmentType.String(),
		config.CreatedAfter,
		config.CreatedBefore,
		time.Now().UTC(),
		time.Now().UTC(),
	).Error)
	seedAudienceSegmentRelationsForTest(t, db, id, config)
}

func seedAudienceSegmentRelationsForTest(
	t *testing.T,
	db *gorm.DB,
	segmentID string,
	config model.AudienceSegmentConfig,
) {
	t.Helper()
	for _, tagID := range config.MemberTagIDs {
		require.NoError(t, db.Create(&model.AudienceSegmentUserTag{
			AudienceSegmentID: segmentID,
			UserTagID:         tagID,
		}).Error)
	}
	for _, role := range config.AccountRoles {
		require.NoError(t, db.Create(&model.AudienceSegmentUserRole{
			AudienceSegmentID: segmentID,
			Role:              role,
		}).Error)
	}
	for _, memberID := range config.ExcludeMemberIDs {
		require.NoError(t, db.Create(&model.AudienceSegmentExcludedMember{
			AudienceSegmentID: segmentID,
			MemberID:          memberID,
		}).Error)
	}
}

func evaluateSingleFileDownloadAccessAt(
	ctx context.Context,
	db *gorm.DB,
	source FileDownloadSource,
	user *auth.UserInfo,
	_ time.Time,
) (bool, error) {
	source = trackDownloadSource(source.FileID, source.Audience)
	return EvaluateFileDownloadAccess(ctx, db, nil, source, user, testSegmentConfigLoader{})
}

func evaluateSingleFileDownloadAccessBatchAt(
	ctx context.Context,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	source FileDownloadSource,
	user *auth.UserInfo,
	_ time.Time,
) (bool, error) {
	source = trackDownloadSource(source.FileID, source.Audience)
	access, err := EvaluateFileDownloadAccessBatch(ctx, db, spiceDB, map[string]FileDownloadSource{source.PolicyKey: source}, user, testSegmentConfigLoader{})
	if err != nil {
		return false, err
	}
	return access[source.PolicyKey], nil
}

func trackDownloadSource(fileID string, audience FileDownloadAudience) FileDownloadSource {
	return FileDownloadSource{
		PolicyKey: fileID, PolicyKind: FileDownloadPolicyTrack,
		TrackID: fileID, FileID: fileID, Audience: audience,
	}
}

type testSegmentConfigLoader struct{}

func (testSegmentConfigLoader) LoadSegmentConfigs(ctx context.Context, db *gorm.DB, segments []*model.AudienceSegment) error {
	return audience.LoadSegmentConfigs(ctx, db, segments)
}

func downloadUserInfo(id string) *auth.UserInfo {
	return &auth.UserInfo{
		IdentityID:    auth.IdentityID(id),
		MemberID:      auth.MemberID(id),
		Authenticated: true,
	}
}
