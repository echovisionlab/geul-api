package filereferences

import (
	"context"

	artistpostgres "github.com/echovisionlab/geul-api/internal/artist/postgres"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"
	formpostgres "github.com/echovisionlab/geul-api/internal/form/postgres"
	labelpostgres "github.com/echovisionlab/geul-api/internal/label/postgres"
	pagepostgres "github.com/echovisionlab/geul-api/internal/page/postgres"
	postpostgres "github.com/echovisionlab/geul-api/internal/post/postgres"
	programeventpostgres "github.com/echovisionlab/geul-api/internal/programevent/postgres"
	referencecatalogpostgres "github.com/echovisionlab/geul-api/internal/referencecatalog/postgres"
	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
	seriespostgres "github.com/echovisionlab/geul-api/internal/series/postgres"
	sitesettingspostgres "github.com/echovisionlab/geul-api/internal/sitesettings/postgres"
	workpostgres "github.com/echovisionlab/geul-api/internal/work/postgres"
	"gorm.io/gorm"
)

// Attachments composes the owners' reference queries without moving their SQL.
type Attachments struct{}

func (Attachments) Queries(ctx context.Context, tx *gorm.DB, fileID any) []filemediapostgres.FileReferenceQuery {
	return []filemediapostgres.FileReferenceQuery{
		{Name: "artist_file", Query: artistpostgres.ArtistFilesReferencingFile(ctx, tx, fileID)},
		{Name: "release_file", Query: releasepostgres.ReleaseFilesReferencingFile(ctx, tx, fileID)},
		{Name: "content_block_attachment", Query: contentblock.ActiveAttachmentsReferencingFile(ctx, tx, fileID)},
		{Name: "program_event_media", Query: programeventpostgres.EventMediaReferencingFile(ctx, tx, fileID)},
		{Name: "track.audio_original_file_id", Query: releasepostgres.TrackOriginalsReferencingFile(ctx, tx, fileID)},
		{Name: "client.logo_file_ids", Query: referencecatalogpostgres.ClientLogosReferencingFile(ctx, tx, fileID)},
		{Name: "label.logo_file_ids", Query: labelpostgres.LabelLogosReferencingFile(ctx, tx, fileID)},
		{Name: "series.featured_image_file_id", Query: seriespostgres.SeriesImagesReferencingFile(ctx, tx, fileID)},
		{Name: "post.featured_image_file_id", Query: postpostgres.PostImagesReferencingFile(ctx, tx, fileID)},
		{Name: "page.featured_image_file_id", Query: pagepostgres.PageImagesReferencingFile(ctx, tx, fileID)},
		{Name: "work.featured_image_file_id", Query: workpostgres.WorkImagesReferencingFile(ctx, tx, fileID)},
		{Name: "form.featured_image_file_id", Query: formpostgres.FormImagesReferencingFile(ctx, tx, fileID)},
		{Name: "map_place.image_file_id", Query: referencecatalogpostgres.MapPlaceImagesReferencingFile(ctx, tx, fileID)},
		{Name: "program_event_series.poster_file_id", Query: programeventpostgres.SeriesPostersReferencingFile(ctx, tx, fileID)},
		{Name: "site_settings.file_ids", Query: sitesettingspostgres.SiteImagesReferencingFile(ctx, tx, fileID)},
		{Name: "site_setting_loader_file", Query: sitesettingspostgres.SiteLoadersReferencingFile(ctx, tx, fileID)},
	}
}
