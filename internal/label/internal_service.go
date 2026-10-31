package label

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"gorm.io/gorm"
)

// LoadLabelBlockDocumentQuery opens the Label document of a collaboration
// room in Locale for the session SessionID.
type LoadLabelBlockDocumentQuery struct {
	LabelID   string
	Locale    string
	SessionID string
}

// LoadLabelBlockDocument returns the room's document: the current source
// graph with the room locale's values overlaid, its locale metadata and the
// Label's document metadata.
func (s *Service) LoadLabelBlockDocument(ctx context.Context, query LoadLabelBlockDocumentQuery) (*intrav1.LoadLabelBlockDocumentResponse, error) {
	if strings.TrimSpace(query.Locale) == "" {
		return nil, domainerr.Required("locale")
	}
	requestedLocale, err := normalizeLabelDocumentLocale(query.Locale)
	if err != nil {
		return nil, err
	}
	var locales []labelLocaleTitle
	var metadata *intrav1.LabelDocumentMetadata
	bootstrap, err := loadLabelContentBlockBootstrap(
		ctx, s.db, s.contentBlocks, s.spiceDB, query.LabelID, query.SessionID,
		func(ctx context.Context, tx *gorm.DB) error {
			var err error
			locales, err = loadLabelLocaleTitles(ctx, tx, query.LabelID)
			if err != nil {
				return err
			}
			metadata, err = loadLabelDocumentMetadata(ctx, tx, query.LabelID)
			return err
		},
	)
	if err != nil {
		return nil, err
	}
	// Collaboration rooms render the current source graph with sparse target
	// values overlaid. Missing target units must therefore remain visible via
	// the source fallback; the sparse projection is reserved for exact
	// translation/DCDP presence reads.
	document, err := contentblock.MaterializeSnapshotRichTextLocale(bootstrap.Snapshot, requestedLocale)
	if err != nil {
		return nil, normalizeLabelContentBlockError(err)
	}
	response := &intrav1.LoadLabelBlockDocumentResponse{
		Document: document, DocumentRevision: bootstrap.Snapshot.Document.Revision.String(),
		SourceMetadata: &intrav1.LabelLocaleMetadata{Locale: bootstrap.Source.SourceLocale},
		Metadata:       metadata, Locale: requestedLocale,
	}
	if err := overlayLabelRoomLocales(response, locales, bootstrap); err != nil {
		return nil, err
	}
	if !response.LocaleExists && targetlocale.SnapshotHasLocale(bootstrap.Snapshot, requestedLocale) {
		return nil, domainerr.Precondition("Label target locale Blocks exist without owning metadata")
	}
	if response.PresentLocaleValues, err = contentblock.PresentRichTextLocaleValues(bootstrap.Snapshot, requestedLocale); err != nil {
		return nil, err
	}
	return response, nil
}

// overlayLabelRoomLocales fills the source title and, when the room locale
// has metadata, its title and target revision.
func overlayLabelRoomLocales(response *intrav1.LoadLabelBlockDocumentResponse, locales []labelLocaleTitle, bootstrap labelContentBootstrap) error {
	sourceLocale := bootstrap.Source.SourceLocale
	for _, locale := range locales {
		if locale.Locale == sourceLocale {
			response.SourceMetadata.Title = locale.Title
		}
		if locale.Locale != response.Locale {
			continue
		}
		response.LocaleExists = true
		response.LocaleMetadata = &intrav1.LabelLocaleMetadata{Locale: response.Locale, Title: locale.Title}
		if response.Locale != sourceLocale {
			revision, err := deriveLabelTargetRevision(bootstrap.Snapshot.Document.Revision.String(), locale.UpdatedAt)
			if err != nil {
				return err
			}
			response.TargetRevision = &revision
		}
	}
	if response.Locale == sourceLocale && !response.LocaleExists {
		return errors.New("Label source locale metadata is missing")
	}
	return nil
}

func loadLabelDocumentMetadata(
	ctx context.Context,
	db *gorm.DB,
	labelID string,
) (*intrav1.LabelDocumentMetadata, error) {
	var label model.Label
	if err := db.WithContext(ctx).
		Select("id", "slug", "country_code", "website", "social_links", "parent_label_id").
		Where("id = ?", labelID).
		Take(&label).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("label", labelID)
		}
		return nil, err
	}
	return &intrav1.LabelDocumentMetadata{
		Slug:          label.Slug,
		CountryCode:   label.CountryCode,
		Website:       label.Website,
		SocialLinks:   label.SocialLinks,
		ParentLabelId: label.ParentLabelID,
	}, nil
}
