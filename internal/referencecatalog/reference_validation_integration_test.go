//go:build integration

package referencecatalog

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/stretchr/testify/require"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestReferenceKindValidationAndUniqueConstraintClassificationIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.PrepareOryIntegrationConcurrentTest(t).DB
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	suffix := testutil.IntegrationUUID()[:8]

	tests := []struct {
		name      string
		maxLength int
		create    func(name string, slug string) error
		update    func(id string, name *string, slug *string) error
	}{
		{
			name:      "genre",
			maxLength: 100,
			create: func(name string, slug string) error {
				_, err := NewGenreService(db, spiceDB).CreateGenre(ctx, connect.NewRequest(&managev1.CreateGenreRequest{Name: name, Slug: &slug}))
				return err
			},
			update: func(id string, name *string, slug *string) error {
				_, err := NewGenreService(db, spiceDB).UpdateGenre(ctx, connect.NewRequest(&managev1.UpdateGenreRequest{Id: id, Name: name, Slug: slug}))
				return err
			},
		},
		{
			name:      "style",
			maxLength: 100,
			create: func(name string, slug string) error {
				_, err := NewStyleService(db, spiceDB).CreateStyle(ctx, connect.NewRequest(&managev1.CreateStyleRequest{Name: name, Slug: &slug}))
				return err
			},
			update: func(id string, name *string, slug *string) error {
				_, err := NewStyleService(db, spiceDB).UpdateStyle(ctx, connect.NewRequest(&managev1.UpdateStyleRequest{Id: id, Name: name, Slug: slug}))
				return err
			},
		},
		{
			name:      "tag",
			maxLength: 50,
			create: func(name string, slug string) error {
				_, err := NewTagService(db, referenceMenuTargetsForTest(nil), spiceDB).CreateTag(ctx, connect.NewRequest(&managev1.CreateTagRequest{Name: name, Slug: &slug}))
				return err
			},
			update: func(id string, name *string, slug *string) error {
				_, err := NewTagService(db, referenceMenuTargetsForTest(nil), spiceDB).UpdateTag(ctx, connect.NewRequest(&managev1.UpdateTagRequest{Id: id, Name: name, Slug: slug}))
				return err
			},
		},
		{
			name:      "format",
			maxLength: 50,
			create: func(name string, slug string) error {
				_, err := NewFormatService(db, spiceDB).CreateFormat(ctx, connect.NewRequest(&managev1.CreateFormatRequest{Name: name, Slug: &slug}))
				return err
			},
			update: func(id string, name *string, slug *string) error {
				_, err := NewFormatService(db, spiceDB).UpdateFormat(ctx, connect.NewRequest(&managev1.UpdateFormatRequest{Id: id, Name: name, Slug: slug}))
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name := "Reference " + test.name + " " + suffix
			slug := "reference-" + test.name + "-" + suffix
			require.NoError(t, test.create(name, slug))
			var id string
			require.NoError(t, db.Table(test.name).Select("id").Where("slug = ?", slug).Scan(&id).Error)
			require.NotEmpty(t, id)
			t.Cleanup(func() {
				_ = db.Exec("DELETE FROM "+test.name+" WHERE id = ?", id).Error
			})

			err := test.create(name, slug+"-other")
			require.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err), "duplicate name must be classified")
			err = test.create(name+" Other", slug)
			require.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err), "duplicate slug must be classified")

			conflictingName := "Conflicting " + test.name + " " + suffix
			conflictingSlug := "conflicting-" + test.name + "-" + suffix
			require.NoError(t, test.create(conflictingName, conflictingSlug))
			var conflictingID string
			require.NoError(t, db.Table(test.name).Select("id").Where("slug = ?", conflictingSlug).Scan(&conflictingID).Error)
			require.NotEmpty(t, conflictingID)
			t.Cleanup(func() {
				_ = db.Exec("DELETE FROM "+test.name+" WHERE id = ?", conflictingID).Error
			})

			err = test.update(id, &conflictingName, nil)
			require.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			require.Equal(t, test.name+" with name '"+name+"' already exists", connectErr.Message())

			err = test.update(id, nil, &conflictingSlug)
			require.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
			connectErr = nil
			require.ErrorAs(t, err, &connectErr)
			require.Equal(t, test.name+" with slug '"+slug+"' already exists", connectErr.Message())

			blank := "   "
			err = test.update(id, &blank, nil)
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

			// The limit is the column length, so one more character is a client
			// error rather than a database failure.
			tooLong := strings.Repeat("가", test.maxLength+1)
			err = test.create(tooLong, slug+"-long")
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			err = test.update(id, nil, &tooLong)
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			err = test.update(id, &tooLong, nil)
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			err = test.create(name+" Blank Slug", blank)
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}
