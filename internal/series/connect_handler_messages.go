package series

import (
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// seriesMessage is the management API form of a Series.
func seriesMessage(detail Detail) *managev1.Series {
	series := detail.Series
	message := &managev1.Series{
		Id:           series.ID,
		Title:        series.Title,
		Slug:         series.Slug,
		Status:       string(series.Status),
		CreatedAt:    timestamppb.New(series.CreatedAt),
		OgAsset:      assetRef(detail.OgAsset),
		SourceLocale: series.SourceLocale,
	}
	if series.Description != nil {
		message.Description = series.Description
	}
	if series.UpdatedAt != nil {
		message.UpdatedAt = timestamppb.New(*series.UpdatedAt)
	}
	if detail.FeaturedImageAsset != nil {
		message.FeaturedImageAsset = assetRef(detail.FeaturedImageAsset)
	}
	return message
}

func seriesWithStatsMessages(items []ListedSeries) []*managev1.SeriesWithStats {
	messages := make([]*managev1.SeriesWithStats, len(items))
	for i := range items {
		messages[i] = &managev1.SeriesWithStats{
			Series:       seriesMessage(items[i].Detail),
			PostCount:    items[i].PostCount,
			ManagerCount: items[i].ManagerCount,
		}
	}
	return messages
}

func paginationMessage(page SeriesPage) *commonv1.PaginationResponse {
	return &commonv1.PaginationResponse{Total: page.Total, Limit: page.Limit, Offset: page.Offset, HasMore: page.HasMore}
}

func seriesSimpleMessages(series []Series) []*managev1.SeriesSimple {
	messages := make([]*managev1.SeriesSimple, len(series))
	for i := range series {
		messages[i] = &managev1.SeriesSimple{Id: series[i].ID, SourceLocale: series[i].SourceLocale, Title: series[i].Title, Slug: series[i].Slug}
	}
	return messages
}

func seriesPostMessages(posts []SeriesPost) []*managev1.SeriesPost {
	messages := make([]*managev1.SeriesPost, len(posts))
	for i := range posts {
		post := posts[i]
		message := &managev1.SeriesPost{Id: post.ID, SourceLocale: post.SourceLocale, Title: post.Title, Status: post.Status}
		if post.Slug != nil {
			message.Slug = *post.Slug
		}
		if post.SeriesOrder != nil {
			message.SeriesOrder = int32(*post.SeriesOrder)
		}
		if post.PublishedAt != nil {
			message.PublishedAt = timestamppb.New(*post.PublishedAt)
		}
		messages[i] = message
	}
	return messages
}

func managerMessages(managers []Manager) []*managev1.SeriesManager {
	messages := make([]*managev1.SeriesManager, 0, len(managers))
	for _, manager := range managers {
		messages = append(messages, &managev1.SeriesManager{
			MemberId:    manager.MemberID,
			Nickname:    manager.Nickname,
			CreatedAt:   timestamppb.New(manager.CreatedAt),
			AvatarAsset: assetRef(manager.Avatar),
		})
	}
	return messages
}

// assetRef is the API form of a ready asset; nil stays nil.
func assetRef(asset *Asset) *commonv1.AssetRef {
	if asset == nil {
		return nil
	}
	return &commonv1.AssetRef{
		AssetId:          asset.ID,
		Url:              asset.URL,
		Extension:        asset.Extension,
		MimeType:         asset.MIMEType,
		FileSize:         asset.FileSize,
		Sha256:           asset.SHA256,
		Disposition:      commonv1.AssetDisposition(asset.Disposition),
		DownloadFilename: asset.DownloadFilename,
	}
}

func localeFieldsMessage(fields LocaleFields) *intrav1.PostSeriesLocaleFields {
	return &intrav1.PostSeriesLocaleFields{Title: fields.Title, Summary: fields.Summary}
}

func requestedLocaleFields(message *intrav1.PostSeriesLocaleFields) *LocaleFields {
	if message == nil {
		return nil
	}
	return &LocaleFields{Title: message.Title, Summary: message.Summary}
}
