package contentsignaladapter

import (
	"context"
	"fmt"

	artistpostgres "github.com/echovisionlab/geul-api/internal/artist/postgres"
	campaignpostgres "github.com/echovisionlab/geul-api/internal/campaign/postgres"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
	formpostgres "github.com/echovisionlab/geul-api/internal/form/postgres"
	labelpostgres "github.com/echovisionlab/geul-api/internal/label/postgres"
	legalpostgres "github.com/echovisionlab/geul-api/internal/legal/postgres"
	menupostgres "github.com/echovisionlab/geul-api/internal/menu/postgres"
	pagepostgres "github.com/echovisionlab/geul-api/internal/page/postgres"
	postpostgres "github.com/echovisionlab/geul-api/internal/post/postgres"
	programeventpostgres "github.com/echovisionlab/geul-api/internal/programevent/postgres"
	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
	seriespostgres "github.com/echovisionlab/geul-api/internal/series/postgres"
	workpostgres "github.com/echovisionlab/geul-api/internal/work/postgres"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

func NewPublisher(publisher contentsignal.Publisher) contentsignal.Publisher {
	return contentsignal.WithRevisionReader(publisher, RevisionReader{})
}

type RevisionReader struct{}

func (RevisionReader) DocumentRevision(ctx context.Context, tx *gorm.DB, entity managev1.ContentEntityType, id string) (string, error) {
	var owner *gorm.DB
	switch entity {
	case managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST:
		owner = postpostgres.PostContentOwner(ctx, tx, id)
	case managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE:
		owner = pagepostgres.PageContentOwner(ctx, tx, id)
	case managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK:
		owner = workpostgres.WorkContentOwner(ctx, tx, id)
	case managev1.ContentEntityType_CONTENT_ENTITY_TYPE_ARTIST:
		owner = artistpostgres.ArtistContentOwner(ctx, tx, id)
	case managev1.ContentEntityType_CONTENT_ENTITY_TYPE_LABEL:
		owner = labelpostgres.LabelContentOwner(ctx, tx, id)
	case managev1.ContentEntityType_CONTENT_ENTITY_TYPE_RELEASE:
		owner = releasepostgres.ReleaseContentOwner(ctx, tx, id)
	case managev1.ContentEntityType_CONTENT_ENTITY_TYPE_FORM:
		owner = formpostgres.FormContentOwner(ctx, tx, id)
	case managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PROGRAM_EVENT:
		owner = programeventpostgres.ProgramEventContentOwner(ctx, tx, id)
	case managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST_SERIES:
		owner = seriespostgres.SeriesContentOwner(ctx, tx, id)
	case managev1.ContentEntityType_CONTENT_ENTITY_TYPE_MENU:
		owner = menupostgres.MenuContentOwner(ctx, tx, id)
	case managev1.ContentEntityType_CONTENT_ENTITY_TYPE_EMAIL_TEMPLATE:
		owner = emailauthoringpostgres.EmailTemplateContentOwner(ctx, tx, id)
	case managev1.ContentEntityType_CONTENT_ENTITY_TYPE_EMAIL_LAYOUT:
		owner = emailauthoringpostgres.EmailLayoutContentOwner(ctx, tx, id)
	case managev1.ContentEntityType_CONTENT_ENTITY_TYPE_CAMPAIGN:
		owner = campaignpostgres.CampaignContentOwner(ctx, tx, id)
	case managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PRIVACY:
		owner = legalpostgres.PrivacyContentOwner(ctx, tx, id)
	case managev1.ContentEntityType_CONTENT_ENTITY_TYPE_TERMS:
		owner = legalpostgres.TermsContentOwner(ctx, tx, id)
	default:
		return "", fmt.Errorf("unknown content.updated entity %s", entity)
	}
	return contentblock.ReadOwnerDocumentRevision(owner)
}
