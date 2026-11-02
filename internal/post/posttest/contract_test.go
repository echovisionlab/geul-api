package posttest_test

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/post/posttest"
)

func TestFakeStoreKeepsThePostStoreContract(t *testing.T) {
	posttest.RunStoreContract(t, func(*testing.T) posttest.Harness { return posttest.FakeHarness() })
}

func TestFakeInsertPostUsesPostgresTimestampPrecision(t *testing.T) {
	store := posttest.NewStore()
	now := time.Date(2026, time.November, 2, 5, 34, 33, 423336010, time.UTC)
	store.Now = func() time.Time { return now }
	value := post.Post{SourceLocale: "en", Status: post.StatusDraft, DocumentLayout: post.DefaultDocumentLayout()}
	require.NoError(t, store.InsertPost(t.Context(), &value))
	expected := now.Truncate(time.Microsecond)
	require.Equal(t, expected, value.CreatedAt)
	require.Equal(t, expected, value.UpdatedAt)
	stored, err := store.FindPost(t.Context(), value.ID)
	require.NoError(t, err)
	require.Equal(t, expected, stored.CreatedAt)
	require.Equal(t, expected, stored.UpdatedAt)
}
