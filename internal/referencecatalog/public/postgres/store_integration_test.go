//go:build integration

package referencecatalogpublicpostgres_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	publicreferencecatalog "github.com/echovisionlab/geul-api/internal/referencecatalog/public"
	referencecatalogpublicpostgres "github.com/echovisionlab/geul-api/internal/referencecatalog/public/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// The public store's aggregate SQL counts only published and archived Posts
// and filters, sorts and pages after aggregating, which only PostgreSQL can
// show.

func newHarness(t *testing.T) (*gorm.DB, mutationtest.Harness[publicreferencecatalog.Store]) {
	t.Helper()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	units, err := referencecatalogpublicpostgres.NewUnits(db, referencecatalogadapter.NewPublicAssets("https://cdn.example.com"))
	require.NoError(t, err)
	return db, mutationtest.PostgresHarness(db, units)
}

func seedPost(t *testing.T, db *gorm.DB, status model.PostStatus, categoryID, tagID string) {
	t.Helper()
	documentID, postID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?::uuid, 'post')`, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO post (id, status, content_document_id) VALUES (?::uuid, ?, ?::uuid)`, postID, string(status), documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO post_category (post_id, category_id) VALUES (?::uuid, ?::uuid)`, postID, categoryID).Error)
	require.NoError(t, db.Exec(`INSERT INTO post_tag (post_id, tag_id) VALUES (?::uuid, ?::uuid)`, postID, tagID).Error)
}

func TestPublicCategoriesAndTagsCountPublicPostsThenFilterSortAndPageIntegration(t *testing.T) {
	t.Parallel()
	db, h := newHarness(t)
	suffix := uuid.NewString()[:8]
	description := "Busy category"
	busyCategory := model.Category{ID: uuid.NewString(), Name: "Busy " + suffix, Slug: "busy-" + suffix, Description: &description}
	quietCategory := model.Category{ID: uuid.NewString(), Name: "Quiet " + suffix, Slug: "quiet-" + suffix}
	busyTag := model.Tag{ID: uuid.NewString(), Name: "Busy " + suffix, Slug: "busy-" + suffix}
	quietTag := model.Tag{ID: uuid.NewString(), Name: "Quiet " + suffix, Slug: "quiet-" + suffix}
	require.NoError(t, db.Create(&[]model.Category{busyCategory, quietCategory}).Error)
	require.NoError(t, db.Create(&[]model.Tag{busyTag, quietTag}).Error)
	seedPost(t, db, model.PostStatusDraft, busyCategory.ID, busyTag.ID)
	seedPost(t, db, model.PostStatusPublished, busyCategory.ID, busyTag.ID)
	seedPost(t, db, model.PostStatusArchived, busyCategory.ID, busyTag.ID)
	seedPost(t, db, model.PostStatusPublished, quietCategory.ID, quietTag.ID)

	byCount := referencecatalog.ListPage{
		Filters: []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: suffix}},
		Sorts:   []*commonv1.SortSpec{{Field: "post_count", Order: commonv1.SortOrder_SORT_ORDER_DESC}},
		Limit:   1,
	}
	require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, store publicreferencecatalog.Store) error {
		categories, total, err := store.Categories(ctx, byCount)
		require.NoError(t, err)
		require.EqualValues(t, 2, total)
		require.Equal(t, []publicreferencecatalog.Category{{
			ID: busyCategory.ID, Name: busyCategory.Name, Slug: busyCategory.Slug, Description: &description, PostCount: 2,
		}}, categories)
		tags, total, err := store.Tags(ctx, byCount)
		require.NoError(t, err)
		require.EqualValues(t, 2, total)
		require.Equal(t, []publicreferencecatalog.Tag{{ID: busyTag.ID, Name: busyTag.Name, Slug: busyTag.Slug, PostCount: 2}}, tags)

		second := byCount
		second.Offset, second.Sorts = 1, nil
		categories, _, err = store.Categories(ctx, second)
		require.NoError(t, err)
		require.Equal(t, quietCategory.ID, categories[0].ID, "the default order is by name")
		require.EqualValues(t, 1, categories[0].PostCount)
		return nil
	}))

	for _, page := range []referencecatalog.ListPage{
		{Filters: []*commonv1.FilterSpec{{Field: "unknown", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "x"}}, Limit: 10},
		{Sorts: []*commonv1.SortSpec{{Field: "unknown", Order: commonv1.SortOrder_SORT_ORDER_ASC}}, Limit: 10},
	} {
		err := mutationtest.Read(t, h, func(ctx context.Context, store publicreferencecatalog.Store) error {
			_, _, err := store.Categories(ctx, page)
			return err
		})
		require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
		err = mutationtest.Read(t, h, func(ctx context.Context, store publicreferencecatalog.Store) error {
			_, _, err := store.Tags(ctx, page)
			return err
		})
		require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	}
}

// seedAsset stores a file and its ready public asset of kind.
func seedAsset(t *testing.T, db *gorm.DB, kind string) string {
	t.Helper()
	fileID, assetID := uuid.NewString(), uuid.NewString()
	digest := sha256.Sum256([]byte(fileID))
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: "public-" + fileID, MimeType: "image/webp", FileSize: 1024, Extension: "webp", SHA256: digest[:], CreatedAt: now,
	}).Error)
	objectKey, err := mediaauth.AssetObjectKey(assetID, "webp")
	require.NoError(t, err)
	fileSize := int64(1024)
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: kind, ObjectKey: objectKey, Extension: "webp", MimeType: "image/webp",
		FileSize: &fileSize, SHA256: digest[:], Disposition: "inline", Status: model.PublicAssetStatusReady,
		ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return fileID
}

func TestPublicClientsAndMapPlacesReadTheirRowsAndReadyAssetsIntegration(t *testing.T) {
	t.Parallel()
	db, h := newHarness(t)
	suffix := uuid.NewString()[:8]
	logoFileID := seedAsset(t, db, "logo")
	imageFileID := seedAsset(t, db, "image")
	clients := []model.Client{
		{ID: uuid.NewString(), Name: "Bravo " + suffix, LogoLightFileID: &logoFileID},
		{ID: uuid.NewString(), Name: "Alpha " + suffix},
	}
	require.NoError(t, db.Create(&clients).Error)
	city := "Seoul"
	place := model.MapPlace{
		ID: uuid.NewString(), Name: "Hall", Address: "Road", Lat: 37.5, Lng: 127, ImageFileID: &imageFileID,
		AddressComponents: &model.AddressComponents{City: &city},
	}
	require.NoError(t, db.Create(&place).Error)

	require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, store publicreferencecatalog.Store) error {
		listed, total, err := store.Clients(ctx, referencecatalog.ListPage{
			Filters: []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: suffix}}, Limit: 10,
		})
		require.NoError(t, err)
		require.EqualValues(t, 2, total)
		require.Equal(t, []string{"Alpha " + suffix, "Bravo " + suffix}, []string{listed[0].Name, listed[1].Name})
		require.Equal(t, logoFileID, *listed[1].LogoLightFileID)

		client, err := store.Client(ctx, clients[0].ID)
		require.NoError(t, err)
		require.Equal(t, listed[1], client)
		_, err = store.Client(ctx, uuid.NewString())
		require.ErrorIs(t, err, referencecatalog.ErrClientNotFound)
		require.NotNil(t, store.ReadyAsset(ctx, referencecatalog.AssetSource{FileID: logoFileID, Kind: "logo"}))

		places, err := store.MapPlacesByID(ctx, []string{place.ID, uuid.NewString()})
		require.NoError(t, err)
		require.Len(t, places, 1)
		require.Equal(t, "Seoul", *places[0].AddressComponents.City)
		require.Nil(t, store.ReadyAsset(ctx, referencecatalog.AssetSource{FileID: imageFileID, Kind: "map_image"}))
		require.NotNil(t, store.ReadyAsset(ctx, referencecatalog.AssetSource{FileID: imageFileID, Kind: "map_image", FallbackKinds: []string{"image"}}),
			"a public map image falls back to a plain image asset")
		return nil
	}))
}
