package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
)

func initializeProgramEventBlockTranslationSource(
	ctx context.Context,
	store Store,
	eventID string,
	sourceLocale string,
	summary *string,
	now time.Time,
) error {
	sourceLocale = strings.TrimSpace(sourceLocale)
	if strings.TrimSpace(eventID) == "" || sourceLocale == "" {
		return errors.New("program Event ID and source locale are required")
	}
	return store.InsertSourceLocale(ctx, eventID, sourceLocale, summary, now)
}

// loadProgramEvent returns the administrator view of an event the caller
// just changed, read under a share lock of the event row.
func (s *Service) loadProgramEvent(ctx context.Context, id string) (EventView, error) {
	var output EventView
	err := s.units.ReadCurrent(ctx, func(store Store) error {
		event, err := lockProgramEventForRead(ctx, store, id)
		if err != nil {
			return err
		}
		output, err = s.programEventView(ctx, store, event)
		return err
	})
	return output, err
}

func lockProgramEventForRead(ctx context.Context, store Store, id string) (Event, error) {
	event, err := store.Event(ctx, id, ShareLock)
	return event, notFoundAs(err, domainerr.ResourceNotFound("program event", id))
}

// loadAuthorizedProgramEvent requires the active principal to view the
// share-locked event and returns its view with authorized Block media.
func (s *Service) loadAuthorizedProgramEvent(ctx context.Context, id string) (EventView, error) {
	var output EventView
	err := s.units.ReadCurrent(ctx, func(store Store) error {
		event, err := lockProgramEventForRead(ctx, store, id)
		if err != nil {
			return err
		}
		if err := requireActiveProgramEventPrincipal(ctx, readLockActive(ctx, store), "view", true); err != nil {
			return err
		}
		if err := requireProgramEventPermission(
			ctx, s.spiceDB, event.ID, programEventViewAction(event.Status),
		); err != nil {
			return err
		}
		output, err = s.programEventView(ctx, store, event)
		if err != nil {
			return err
		}
		documentID, err := loadProgramEventContentDocumentID(ctx, store, event.ID, NoLock)
		if err != nil {
			return err
		}
		output.BlockMedia, err = store.HydrateBlockMedia(
			ctx, event.ID, documentID, auth.GetUser(ctx), output.BlockMedia,
		)
		return err
	})
	return output, err
}

func (s *Service) programEventView(ctx context.Context, store Store, event Event) (EventView, error) {
	documentID, err := loadProgramEventContentDocumentID(ctx, store, event.ID, NoLock)
	if err != nil {
		return EventView{}, err
	}
	snapshot, err := store.ContentSnapshot(ctx, documentID, event.SourceLocale)
	if err != nil {
		return EventView{}, normalizeProgramEventContentBlockError(err)
	}
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return EventView{}, normalizeProgramEventContentBlockError(err)
	}
	blockMedia, err := store.MediaReferences(ctx, documentID)
	if err != nil {
		return EventView{}, err
	}
	locales, err := loadProgramEventLocales(ctx, store, event.ID, snapshot, contentBlockFileRenderResolver{store: store})
	if err != nil {
		return EventView{}, err
	}
	view := EventView{Event: event, Document: document, DocumentRevision: snapshot.Document.Revision.String(), BlockMedia: blockMedia, Locales: locales}
	for _, relation := range []struct {
		kind RelationKind
		into *[]Relation
	}{
		{ArtistRelation, &view.Artists},
		{LabelRelation, &view.Labels},
		{ClientRelation, &view.Clients},
	} {
		if *relation.into, err = store.Relations(ctx, relation.kind, event.ID); err != nil {
			return EventView{}, err
		}
	}
	if view.Credits, err = s.loadProgramEventCredits(ctx, store, event.ID); err != nil {
		return EventView{}, err
	}
	if view.Media, err = store.EventMedia(ctx, event.ID); err != nil {
		return EventView{}, err
	}
	posterFileID, err := store.PrimaryMediaFileID(ctx, event.ID, "poster")
	if err != nil {
		return EventView{}, err
	}
	if posterFileID != "" {
		view.PosterFileID = &posterFileID
	}
	return view, nil
}

func loadProgramEventLocales(
	ctx context.Context,
	store Store,
	eventID string,
	snapshot contentblock.Snapshot,
	resolver contentblock.FileRenderResolver,
) ([]LocaleView, error) {
	rows, err := store.LocaleSummaries(ctx, eventID)
	if err != nil {
		return nil, err
	}
	result := make([]LocaleView, 0, len(rows))
	for i := range rows {
		document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, rows[i].Locale)
		if err != nil {
			return nil, normalizeProgramEventContentBlockError(err)
		}
		var variables sitevariable.Context
		if contentblock.HasSiteVariables(document) {
			variables, err = store.SiteVariables(ctx)
			if err != nil {
				return nil, err
			}
		}
		projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, resolver, variables)
		if err != nil {
			return nil, normalizeProgramEventContentBlockError(err)
		}
		result = append(result, LocaleView{
			Locale: rows[i].Locale, Summary: rows[i].Summary,
			ContentHTML: projection.HTML, ContentText: projection.Text,
		})
	}
	return result, nil
}
