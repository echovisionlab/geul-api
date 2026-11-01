package label

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// LoadLabelBlockDocumentQuery opens the Label document of a collaboration
// room in Locale for the session SessionID.
type LoadLabelBlockDocumentQuery struct {
	LabelID   string
	Locale    string
	SessionID string
}

// LocaleMetadata is the title of one Label locale a room shows.
type LocaleMetadata struct {
	Locale string
	Title  *string
}

// RoomDocument is a collaboration room's Label document: the current source
// graph with the room locale's values overlaid, its locale metadata and the
// Label's document metadata.
type RoomDocument struct {
	Document            *contentv1.LocalizedRichTextDocument
	DocumentRevision    string
	Metadata            DocumentMetadata
	SourceMetadata      LocaleMetadata
	Locale              string
	LocaleExists        bool
	LocaleMetadata      *LocaleMetadata
	TargetRevision      *string
	PresentLocaleValues []*managev1.AIDocumentFieldTarget
}

// LoadLabelBlockDocument returns the room's document.
func (s *Service) LoadLabelBlockDocument(ctx context.Context, query LoadLabelBlockDocumentQuery) (RoomDocument, error) {
	if strings.TrimSpace(query.Locale) == "" {
		return RoomDocument{}, domainerr.Required("locale")
	}
	requestedLocale, err := normalizeLabelDocumentLocale(query.Locale)
	if err != nil {
		return RoomDocument{}, err
	}
	var locales []LocaleTitle
	var metadata DocumentMetadata
	bootstrap, err := s.loadLabelContentBlockBootstrap(ctx, query.LabelID, query.SessionID, func(ctx context.Context, store Store) error {
		var err error
		locales, err = store.LocaleTitles(ctx, query.LabelID)
		if err != nil {
			return err
		}
		metadata, err = store.DocumentMetadata(ctx, query.LabelID)
		if errors.Is(err, ErrLabelNotFound) {
			return domainerr.ResourceNotFound("label", query.LabelID)
		}
		return err
	})
	if err != nil {
		return RoomDocument{}, err
	}
	// Collaboration rooms render the current source graph with sparse target
	// values overlaid. Missing target units must therefore remain visible via
	// the source fallback; the sparse projection is reserved for exact
	// translation/DCDP presence reads.
	document, err := contentblock.MaterializeSnapshotRichTextLocale(bootstrap.Snapshot, requestedLocale)
	if err != nil {
		return RoomDocument{}, normalizeLabelContentBlockError(err)
	}
	room := RoomDocument{
		Document: document, DocumentRevision: bootstrap.Snapshot.Document.Revision.String(),
		SourceMetadata: LocaleMetadata{Locale: bootstrap.SourceLocale},
		Metadata:       metadata, Locale: requestedLocale,
	}
	if err := overlayLabelRoomLocales(&room, locales, bootstrap); err != nil {
		return RoomDocument{}, err
	}
	if !room.LocaleExists && targetlocale.SnapshotHasLocale(bootstrap.Snapshot, requestedLocale) {
		return RoomDocument{}, domainerr.Precondition("Label target locale Blocks exist without owning metadata")
	}
	if room.PresentLocaleValues, err = contentblock.PresentRichTextLocaleValues(bootstrap.Snapshot, requestedLocale); err != nil {
		return RoomDocument{}, err
	}
	return room, nil
}

// overlayLabelRoomLocales fills the source title and, when the room locale
// has metadata, its title and target revision.
func overlayLabelRoomLocales(room *RoomDocument, locales []LocaleTitle, bootstrap labelContentBootstrap) error {
	sourceLocale := bootstrap.SourceLocale
	for _, locale := range locales {
		if locale.Locale == sourceLocale {
			room.SourceMetadata.Title = locale.Title
		}
		if locale.Locale != room.Locale {
			continue
		}
		room.LocaleExists = true
		room.LocaleMetadata = &LocaleMetadata{Locale: room.Locale, Title: locale.Title}
		if room.Locale != sourceLocale {
			revision, err := deriveLabelTargetRevision(bootstrap.Snapshot.Document.Revision.String(), locale.UpdatedAt)
			if err != nil {
				return err
			}
			room.TargetRevision = &revision
		}
	}
	if room.Locale == sourceLocale && !room.LocaleExists {
		return errors.New("Label source locale metadata is missing")
	}
	return nil
}
