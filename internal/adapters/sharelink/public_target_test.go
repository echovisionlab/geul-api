package sharelinkadapter

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/crypto"
	sharelinkpublic "github.com/echovisionlab/geul-api/internal/sharelink/public"
)

func TestShareLinkValidateRequiresContractedExistingTarget(t *testing.T) {
	t.Parallel()

	db := newShareLinkValidationUnitDB(t)
	service := sharelinkpublic.NewService(db, NewPublicTargetResolver(db))

	privacyID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO privacy_history (id, status, effective_from) VALUES (?, ?, ?)`, privacyID, managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(), time.Now().Add(time.Hour)).Error)
	insertShareLinkValidationFixture(t, db, "privacy-token", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY.String(), privacyID)
	privacy := validateShareLinkUnit(t, service, "privacy-token")
	require.True(t, privacy.Valid)
	require.Nil(t, privacy.Slug)

	protectedPrivacyID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO privacy_history (id, status, effective_from) VALUES (?, ?, ?)`, protectedPrivacyID, managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(), time.Now().Add(time.Hour)).Error)
	passwordHash, err := crypto.NewPasswordHasher(nil).Hash("secret")
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		INSERT INTO share_link (
			id, token, entity_type, entity_id, password_hash, expires_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, uuid.NewString(), "protected-privacy-token", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY.String(), protectedPrivacyID, passwordHash, time.Now().Add(time.Hour)).Error)
	protected := validateShareLinkUnit(t, service, "protected-privacy-token")
	require.False(t, protected.Valid)
	require.True(t, protected.PasswordRequired)
	require.Equal(t, protectedPrivacyID, protected.GetEntityId())
	require.Equal(t, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY, protected.GetEntityType())
	password := "secret"
	opened, err := service.Validate(context.Background(), connect.NewRequest(&openv1.ValidateShareLinkRequest{
		Token: "protected-privacy-token", Password: &password,
	}))
	require.NoError(t, err)
	require.True(t, opened.Msg.Valid)

	insertShareLinkValidationFixture(t, db, "dangling-token", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE.String(), uuid.NewString())
	require.False(t, validateShareLinkUnit(t, service, "dangling-token").Valid)

	insertShareLinkValidationFixture(t, db, "unsupported-token", "SHARE_LINK_ENTITY_TYPE_UNSPECIFIED", uuid.NewString())
	require.False(t, validateShareLinkUnit(t, service, "unsupported-token").Valid)

	insertShareLinkValidationFixture(t, db, "corrupt-token", "not-an-enum", uuid.NewString())
	require.False(t, validateShareLinkUnit(t, service, "corrupt-token").Valid)
}

func validateShareLinkUnit(t *testing.T, service *sharelinkpublic.ShareLinkService, token string) *openv1.ValidateShareLinkResponse {
	t.Helper()
	response, err := service.Validate(context.Background(), connect.NewRequest(&openv1.ValidateShareLinkRequest{Token: token}))
	require.NoError(t, err)
	return response.Msg
}

func insertShareLinkValidationFixture(t *testing.T, db *gorm.DB, token, entityType, entityID string) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO share_link (
			id, token, entity_type, entity_id, expires_at, created_at
		) VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, uuid.NewString(), token, entityType, entityID, time.Now().Add(time.Hour)).Error)
}

func newShareLinkValidationUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE share_link (
			id TEXT PRIMARY KEY,
			token TEXT NOT NULL UNIQUE,
			entity_type TEXT NOT NULL,
			entity_id TEXT NOT NULL,
			label TEXT,
			password_hash TEXT,
			expires_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL
		);
		CREATE TABLE post (id TEXT PRIMARY KEY, slug TEXT);
		CREATE TABLE page (id TEXT PRIMARY KEY, slug TEXT);
		CREATE TABLE work (id TEXT PRIMARY KEY, slug TEXT);
		CREATE TABLE form (id TEXT PRIMARY KEY, slug TEXT);
		CREATE TABLE privacy_history (id TEXT PRIMARY KEY, status TEXT NOT NULL, effective_from DATETIME);
		CREATE TABLE terms_history (id TEXT PRIMARY KEY, status TEXT NOT NULL, effective_from DATETIME);
	`).Error)
	return db
}
