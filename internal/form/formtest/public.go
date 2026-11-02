package formtest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/form/public"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// PublicHarness is one public.Store implementation the public contract runs
// against, with the rows it reads seeded through the Form harness.
type PublicHarness struct {
	mutationtest.Harness[public.Store]
	// Forms seeds Forms, locales and submissions.
	Forms Harness
	// SeedShareLink stores a share link and returns its token.
	SeedShareLink func(t *testing.T, link public.ShareLink) string
}

// FakePublicHarness is the PublicHarness of the fake Store.
func FakePublicHarness() PublicHarness {
	store := NewStore()
	return PublicHarness{
		Harness: mutationtest.FakeHarness[public.Store](store),
		Forms:   fakeHarness(store),
		SeedShareLink: func(_ *testing.T, link public.ShareLink) string {
			token := uuid.NewString()
			store.PutShareLink(token, link)
			return token
		},
	}
}

// RunPublicStoreContract runs every public Store contract case against a
// fresh PublicHarness, in parallel.
func RunPublicStoreContract(t *testing.T, open func(t *testing.T) PublicHarness) {
	t.Helper()
	for name, run := range publicContract {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run(t, open(t))
		})
	}
}

func transactPublic(t *testing.T, h PublicHarness, work func(ctx context.Context, store public.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, work))
}

var publicContract = map[string]func(t *testing.T, h PublicHarness){
	"a visitor's reads answer their sentinels": func(t *testing.T, h PublicHarness) {
		missing := uuid.NewString()
		transactPublic(t, h, func(ctx context.Context, store public.Store) error {
			_, err := store.FindForm(ctx, form.ByID, missing)
			require.ErrorIs(t, err, form.ErrFormNotFound)
			_, err = store.LockForm(ctx, missing)
			require.ErrorIs(t, err, form.ErrFormNotFound)
			_, err = store.SourceLocale(ctx, missing)
			require.ErrorIs(t, err, form.ErrFormNotFound)
			_, err = store.LocaleState(ctx, missing, "en")
			require.ErrorIs(t, err, form.ErrLocaleNotFound)
			_, err = store.ShareLink(ctx, "missing-"+missing)
			require.ErrorIs(t, err, public.ErrShareLinkNotFound)
			return nil
		})
	},
	"submissions count, match their Member and list newest first": func(t *testing.T, h PublicHarness) {
		inserted := insert(t, h.Forms, nil)
		memberID := h.Forms.SeedMember(t)
		at := stamp().Add(-time.Hour)
		older := submit(t, h.Forms, form.Submission{FormID: inserted.ID, MemberID: &memberID, Data: []byte(`{"a":"b"}`), CreatedAt: at})
		var newer form.Submission
		transactPublic(t, h, func(ctx context.Context, store public.Store) error {
			newer = form.Submission{FormID: inserted.ID, Data: []byte(`{"a":"c"}`), IPAddress: text("203.0.113.1"), CreatedAt: stamp()}
			require.NoError(t, store.InsertSubmission(ctx, &newer))
			require.NotEmpty(t, newer.ID)
			return nil
		})
		transactPublic(t, h, func(ctx context.Context, store public.Store) error {
			count, err := store.CountSubmissions(ctx, inserted.ID)
			require.NoError(t, err)
			require.Equal(t, int64(2), count)
			submitted, err := store.HasMemberSubmission(ctx, inserted.ID, memberID)
			require.NoError(t, err)
			require.True(t, submitted)
			submitted, err = store.HasMemberSubmission(ctx, inserted.ID, uuid.NewString())
			require.NoError(t, err)
			require.False(t, submitted)
			listed, err := store.Submissions(ctx, inserted.ID)
			require.NoError(t, err)
			require.Equal(t, []string{newer.ID, older.ID}, []string{listed[0].ID, listed[1].ID})
			require.Equal(t, "203.0.113.1", *listed[0].IPAddress)
			return nil
		})
	},
	"a share link reads back as stored": func(t *testing.T, h PublicHarness) {
		inserted := insert(t, h.Forms, nil)
		expires := stamp().Add(time.Hour)
		token := h.SeedShareLink(t, public.ShareLink{EntityID: inserted.ID, EntityType: public.ShareLinkFormDashboard, ExpiresAt: &expires})
		transactPublic(t, h, func(ctx context.Context, store public.Store) error {
			link, err := store.ShareLink(ctx, token)
			require.NoError(t, err)
			require.Equal(t, inserted.ID, link.EntityID)
			require.Equal(t, public.ShareLinkFormDashboard, link.EntityType)
			require.True(t, expires.Equal(*link.ExpiresAt))
			require.Nil(t, link.PasswordHash)
			return nil
		})
	},
	"a form locked for a submission makes a second submission wait": func(t *testing.T, h PublicHarness) {
		inserted := insert(t, h.Forms, nil)
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store public.Store) error {
				_, err := store.LockForm(ctx, inserted.ID)
				return err
			},
			func(ctx context.Context, store public.Store) error {
				return store.InsertSubmission(ctx, &form.Submission{FormID: inserted.ID, Data: []byte(`{}`), CreatedAt: stamp()})
			},
			func(ctx context.Context, store public.Store) error {
				if _, err := store.LockForm(ctx, inserted.ID); err != nil {
					return err
				}
				count, err := store.CountSubmissions(ctx, inserted.ID)
				if err != nil {
					return err
				}
				require.Equal(t, int64(1), count, "the second submission counts the first")
				return nil
			},
		)
		require.NoError(t, err)
	},
}
