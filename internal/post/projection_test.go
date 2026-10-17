package post

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestManagePostProjectionIncludesSourceLocale(t *testing.T) {
	for _, locale := range []string{"ko", "en", "pt-BR", "zh-TW"} {
		t.Run(locale, func(t *testing.T) {
			service := &PostService{}
			response := service.toProtoPost(&model.Post{
				SourceLocale: locale, DocumentLayout: model.DefaultDocumentLayout(),
				Series: &model.Series{SourceLocale: "ja"},
			}, nil)
			if response.GetSourceLocale() != locale {
				t.Fatalf("source locale = %q, want %q", response.GetSourceLocale(), locale)
			}
			// Exercise the wire response consumed before the editor snapshot RPC.
			wire, err := protojson.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			decoded := new(managev1.Post)
			if err := protojson.Unmarshal(wire, decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.GetSourceLocale() != locale {
				t.Fatalf("wire source locale missing: %s", wire)
			}
			if decoded.GetSeries().GetSourceLocale() != "ja" {
				t.Fatalf("nested Series must keep its own source locale: %s", wire)
			}
		})
	}
}

func TestManagePostWireResponseKeepsContentRelationsAndAuthority(t *testing.T) {
	id := uuid.NewString()
	revision, configurationRevision, mapPlaceID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	layout := model.DefaultDocumentLayout()
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	document := &contentv1.RichTextDocument{SourceLocale: "ko"}
	media := []*contentv1.ContentBlockMediaItem{{Delivery: &commonv1.MediaDelivery{FileId: uuid.NewString()}}}
	can, err := policyv1.Post.Edit(id)
	require.NoError(t, err)
	view, err := policyv1.Post.View(id)
	require.NoError(t, err)
	author := &commonv1.MemberSummary{Id: uuid.NewString(), Nickname: "Author"}
	featured := &commonv1.MediaDelivery{FileId: uuid.NewString()}
	og := &commonv1.AssetRef{AssetId: uuid.NewString(), Url: "https://cdn.example/og.webp"}
	response := new(PostService).toProtoPostWithProjection(&model.Post{
		ID: id, Title: "Title", Slug: new("post"), Summary: new("Summary"), SourceLocale: "ko",
		ContentDocument: document, ContentRevision: revision, ConfigurationRevision: configurationRevision,
		BlockMedia: media, Status: model.PostStatus(managev1.PostStatus_POST_STATUS_DRAFT.String()),
		CommentsEnabled: true, DocumentLayout: layout,
		CreatedAt: now, UpdatedAt: now, PublishedAt: &now, ScheduledAt: &now, ScheduledTimeZone: new("Asia/Seoul"),
		SeriesOrder: new(int32(3)), MapPlaceID: &mapPlaceID,
		Categories: []model.Category{{ID: "category-1", Name: "Category", Slug: "category", CreatedAt: now}},
		Tags:       []model.Tag{{ID: "tag-1", Name: "Tag", Slug: "tag", CreatedAt: now}},
		Series:     &model.Series{ID: "series-1", Title: "Series", Slug: "series", SourceLocale: "ja", Status: "published", CreatedAt: now},
	}, og, []*commonv1.MemberSummary{author}, featured, postAuthority(view, can))
	wire, err := protojson.Marshal(response)
	require.NoError(t, err)
	decoded := new(managev1.Post)
	require.NoError(t, protojson.Unmarshal(wire, decoded))
	require.Equal(t, id, decoded.Id)
	require.Equal(t, "Title", decoded.Title)
	require.Equal(t, "post", decoded.GetSlug())
	require.Equal(t, "Summary", decoded.GetSummary())
	require.Equal(t, "ko", decoded.SourceLocale)
	require.True(t, proto.Equal(document, decoded.Document))
	require.Equal(t, revision, decoded.Revision)
	require.Equal(t, configurationRevision, decoded.ConfigurationRevision)
	require.True(t, proto.Equal(media[0], decoded.BlockMedia[0]))
	require.True(t, proto.Equal(author, decoded.AuthorMembers[0]))
	require.True(t, proto.Equal(featured, decoded.FeaturedImageDelivery))
	require.True(t, proto.Equal(og, decoded.OgAsset))
	require.True(t, proto.Equal(layout.Proto(), decoded.DocumentLayout))
	require.True(t, decoded.CommentsEnabled)
	require.Equal(t, managev1.PostStatus_POST_STATUS_DRAFT, decoded.Status)
	require.Contains(t, decoded.AllowedActions, managev1.PostAction_POST_ACTION_EDIT)
	for _, timestamp := range []time.Time{decoded.CreatedAt.AsTime(), decoded.UpdatedAt.AsTime(), decoded.PublishedAt.AsTime(), decoded.ScheduledAt.AsTime(), decoded.Categories[0].CreatedAt.AsTime(), decoded.Tags[0].CreatedAt.AsTime(), decoded.Series.CreatedAt.AsTime()} {
		require.Equal(t, now, timestamp)
	}
	require.Equal(t, "Asia/Seoul", decoded.GetScheduledTimeZone())
	require.EqualValues(t, 3, decoded.GetSeriesOrder())
	require.Equal(t, mapPlaceID, decoded.GetMapPlaceId())
	require.Equal(t, "category-1", decoded.Categories[0].Id)
	require.Equal(t, "tag-1", decoded.Tags[0].Id)
	require.Equal(t, "series-1", decoded.Series.Id)
	require.Equal(t, "ja", decoded.Series.SourceLocale)
	require.Equal(t, "published", decoded.Series.Status)
}
