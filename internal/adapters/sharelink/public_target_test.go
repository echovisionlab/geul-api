package sharelinkadapter

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	sharelinkpublic "github.com/echovisionlab/geul-api/internal/sharelink/public"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestPublicTargetResolverFindsContractedExistingTargets(t *testing.T) {
	t.Parallel()
	db := newShareLinkAdapterUnitDB(t)
	resolver := NewPublicTargetResolver(db)
	now := time.Now()
	artistID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO artist (id, slug) VALUES (?, ?)`, artistID, "artist-slug").Error)
	scheduledID, activeID, pastID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO privacy_history (id, status, effective_from) VALUES (?, ?, ?), (?, ?, ?), (?, ?, ?)`,
		scheduledID, model.PrivacyStatusScheduled, now.Add(time.Hour),
		activeID, model.PrivacyStatusActive, now.Add(-time.Hour),
		pastID, model.PrivacyStatusScheduled, now.Add(-time.Hour)).Error)

	for name, tc := range map[string]struct {
		entityType sharelink.EntityType
		entityID   string
		want       sharelinkpublic.Target
	}{
		"an existing artist":            {sharelink.EntityArtist, artistID, sharelinkpublic.Target{Exists: true, Slug: stringPointer("artist-slug")}},
		"a missing page":                {sharelink.EntityPage, uuid.NewString(), sharelinkpublic.Target{}},
		"a future scheduled version":    {sharelink.EntityPrivacy, scheduledID, sharelinkpublic.Target{Exists: true}},
		"an active version":             {sharelink.EntityPrivacy, activeID, sharelinkpublic.Target{Exists: true, PublicHistory: true}},
		"a schedule that already began": {sharelink.EntityPrivacy, pastID, sharelinkpublic.Target{}},
		"an unknown target kind":        {"not-an-enum", artistID, sharelinkpublic.Target{}},
	} {
		target, err := resolver.Resolve(t.Context(), tc.entityType, tc.entityID, now)
		require.NoError(t, err, name)
		require.Equal(t, tc.want, target, name)
	}
}

// ValidateForEntity keeps the not-found answer the private reads of other
// domains branch on.
func TestValidateForEntityAnswersEveryUnopenedProofAsRecordNotFound(t *testing.T) {
	t.Parallel()
	db := newShareLinkAdapterUnitDB(t)
	labelID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO share_link (id, token, entity_type, entity_id, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), "label-token", string(sharelink.EntityLabel), labelID, time.Now().Add(time.Hour), time.Now()).Error)

	link, err := ValidateForEntity(t.Context(), db, "label-token", "", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_LABEL, labelID)
	require.NoError(t, err)
	require.Equal(t, "label-token", link.Token)
	require.Equal(t, string(sharelink.EntityLabel), link.EntityType)
	_, err = ValidateForEntity(t.Context(), db, "label-token", "", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_ARTIST, labelID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = ValidateForEntity(t.Context(), db, "label-token", "", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_UNSPECIFIED, labelID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func stringPointer(value string) *string { return &value }

func newShareLinkAdapterUnitDB(t *testing.T) *gorm.DB {
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
		CREATE TABLE release (id TEXT PRIMARY KEY, slug TEXT);
		CREATE TABLE form (id TEXT PRIMARY KEY, slug TEXT);
		CREATE TABLE label (id TEXT PRIMARY KEY, slug TEXT);
		CREATE TABLE artist (id TEXT PRIMARY KEY, slug TEXT);
		CREATE TABLE privacy_history (id TEXT PRIMARY KEY, status TEXT NOT NULL, effective_from DATETIME);
		CREATE TABLE terms_history (id TEXT PRIMARY KEY, status TEXT NOT NULL, effective_from DATETIME);
	`).Error)
	return db
}
