package sharelink_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	"github.com/echovisionlab/geul-api/internal/sharelink/sharelinktest"
)

func TestVerifyLinkOpensOnlyItsLiveTargetWithItsPassword(t *testing.T) {
	t.Parallel()
	store := sharelinktest.NewStore()
	postID := uuid.NewString()
	open := store.SeedLink(sharelink.Link{Token: "open", EntityType: sharelink.EntityPost, EntityID: postID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)})
	store.SeedLink(sharelink.Link{Token: "expired", EntityType: sharelink.EntityPost, EntityID: postID, CreatedAt: time.Now().Add(-2 * time.Hour), ExpiresAt: time.Now().Add(-time.Hour)})
	hash, err := crypto.NewPasswordHasher(nil).Hash("secret")
	require.NoError(t, err)
	protected := store.SeedLink(sharelink.Link{Token: "protected", EntityType: sharelink.EntityPost, EntityID: postID, PasswordHash: &hash, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)})
	verify := func(token, password string, entityType sharelink.EntityType, entityID string) (sharelink.Link, error) {
		return sharelink.VerifyLink(t.Context(), store, token, password, entityType, entityID)
	}

	link, err := verify("open", "", sharelink.EntityPost, postID)
	require.NoError(t, err)
	require.Equal(t, open, link)
	link, err = verify("protected", "secret", sharelink.EntityPost, postID)
	require.NoError(t, err)
	require.Equal(t, protected, link)

	for name, proof := range map[string]struct {
		token, password string
		entityType      sharelink.EntityType
		entityID        string
	}{
		"an expired link":     {"expired", "", sharelink.EntityPost, postID},
		"another target kind": {"open", "", sharelink.EntityPage, postID},
		"another target":      {"open", "", sharelink.EntityPost, uuid.NewString()},
		"an unknown token":    {"missing", "", sharelink.EntityPost, postID},
		"a missing password":  {"protected", "", sharelink.EntityPost, postID},
		"a wrong password":    {"protected", "guess", sharelink.EntityPost, postID},
	} {
		_, err := verify(proof.token, proof.password, proof.entityType, proof.entityID)
		require.ErrorIs(t, err, sharelink.ErrLinkNotFound, name)
	}
}
