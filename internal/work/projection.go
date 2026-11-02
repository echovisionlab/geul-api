package work

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/pathslug"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
)

// View is a Work as the management surface shows it: the row with its
// source-locale title, summary and OG image, its content projection, and
// the assets and clients a read attaches.
type View struct {
	Work
	Title         string
	Summary       *string
	OgAssetID     *string
	OgAsset       *mediaasset.Reference
	FeaturedImage *mediaasset.Reference
	Revision      string
	Document      *contentv1.RichTextDocument
	BlockMedia    []*contentv1.ContentBlockMediaItem
	Clients       []Client
}

// Client is a client of a Work with its ready logos.
type Client struct {
	ID        string
	Name      string
	Website   *string
	LightLogo *mediaasset.Reference
	DarkLogo  *mediaasset.Reference
}

// viewOf overlays the Work's source-locale title, summary and OG image.
func viewOf(work Work, source *SourceDocument) View {
	view := View{Work: work}
	if source == nil {
		return view
	}
	if source.Title != nil {
		view.Title = *source.Title
	}
	if source.Summary != nil {
		view.Summary = source.Summary
	}
	view.OgAssetID = source.OgAssetID
	return view
}

func collectWorkIDs(works []Work) []string {
	ids := make([]string, 0, len(works))
	for _, work := range works {
		ids = append(ids, work.ID)
	}
	return ids
}

func (s *Service) loadSourceDocuments(ctx context.Context, workIDs []string) (map[string]SourceDocument, error) {
	var documents map[string]SourceDocument
	err := s.read(ctx, func(store Store) error {
		var err error
		documents, err = store.SourceDocuments(ctx, workIDs)
		return err
	})
	return documents, err
}

// viewWithSourceDocument reads the Work's source-locale document state and
// overlays it.
func (s *Service) viewWithSourceDocument(ctx context.Context, work Work) (View, error) {
	documents, err := s.loadSourceDocuments(ctx, []string{work.ID})
	if err != nil {
		return View{}, err
	}
	source, ok := documents[work.ID]
	if !ok {
		return viewOf(work, nil), nil
	}
	return viewOf(work, &source), nil
}

// CheckWorkSlugAvailableQuery asks whether Slug is free for a new Work, or
// for ExcludeWorkID's own Work.
type CheckWorkSlugAvailableQuery struct {
	Slug          string
	ExcludeWorkID *string
}

// CheckWorkSlugAvailable reports whether no other Work and no route use the
// slug.
func (s *Service) CheckWorkSlugAvailable(ctx context.Context, query CheckWorkSlugAvailableQuery) (bool, error) {
	if query.ExcludeWorkID != nil {
		work, err := s.findWork(ctx, *query.ExcludeWorkID)
		if err != nil {
			return false, err
		}
		if err := requireWorkPermission(ctx, s.spiceDB, work, policyv1.Work.Manage, workAuthorizationMutation); err != nil {
			return false, err
		}
	} else if err := requireWorkCreate(ctx, s.spiceDB); err != nil {
		return false, err
	}
	if err := pathslug.Validate(query.Slug); err != nil {
		return false, err
	}

	var available bool
	err := s.read(ctx, func(store Store) error {
		taken, err := store.SlugTaken(ctx, query.Slug, query.ExcludeWorkID)
		if err != nil || taken {
			return err
		}
		available, err = store.ResourceRouteAvailable(ctx, query.Slug)
		return err
	})
	return available, err
}

func (s *Service) getWorkFeaturedImageAsset(ctx context.Context, workID string) *mediaasset.Reference {
	var fileID *string
	if err := s.read(ctx, func(store Store) error {
		var err error
		fileID, err = store.FeaturedImageFileID(ctx, workID)
		if errors.Is(err, ErrWorkNotFound) {
			fileID, err = nil, nil
		}
		return err
	}); err != nil {
		slog.ErrorContext(ctx, "Work featured image lookup failed; serving work without featured image", "domain", "work", "operation", "load_featured_image", failurecause.Attr(err), "work_id", workID)
		return nil
	}
	if fileID == nil || *fileID == "" {
		return nil
	}

	asset, err := s.readyFileAsset(ctx, *fileID, "image")
	if err != nil {
		slog.ErrorContext(ctx, "Work featured image asset resolution failed; serving work without featured image", "domain", "work", "operation", "resolve_featured_image_asset", failurecause.Attr(err), "work_id", workID, "file_id", *fileID)
		return nil
	}
	return asset
}

func (s *Service) readyFileAsset(ctx context.Context, fileID, kind string) (*mediaasset.Reference, error) {
	var asset *mediaasset.Reference
	err := s.read(ctx, func(store Store) error {
		var err error
		asset, err = store.ReadyFileAsset(ctx, fileID, kind)
		return err
	})
	return asset, err
}

func (s *Service) getWorkClients(ctx context.Context, workID string) ([]Client, error) {
	var rows []ClientRow
	if err := s.read(ctx, func(store Store) error {
		var err error
		rows, err = store.Clients(ctx, workID)
		return err
	}); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	clients := make([]Client, 0, len(rows))
	for _, row := range rows {
		client := Client{ID: row.ID, Name: row.Name, Website: row.Website}
		if row.LightFileID != nil && *row.LightFileID != "" {
			asset, assetErr := s.readyFileAsset(ctx, *row.LightFileID, "logo")
			if assetErr != nil {
				slog.ErrorContext(ctx, "Work client light logo resolution failed; serving client without light logo", "domain", "work", "operation", "resolve_client_logo_asset", failurecause.Attr(assetErr), "work_id", workID, "client_id", row.ID, "file_id", *row.LightFileID)
			} else {
				client.LightLogo = asset
			}
		}
		if row.DarkFileID != nil && *row.DarkFileID != "" {
			asset, assetErr := s.readyFileAsset(ctx, *row.DarkFileID, "logo")
			if assetErr != nil {
				slog.ErrorContext(ctx, "Work client dark logo resolution failed; serving client without dark logo", "domain", "work", "operation", "resolve_client_logo_asset", failurecause.Attr(assetErr), "work_id", workID, "client_id", row.ID, "file_id", *row.DarkFileID)
			} else {
				client.DarkLogo = asset
			}
		}
		clients = append(clients, client)
	}

	return clients, nil
}

func (s *Service) loadReadyWorkOgAssets(ctx context.Context, works []View) (map[string]*mediaasset.Reference, error) {
	candidates := make([]*string, 0, len(works))
	for i := range works {
		candidates = append(candidates, works[i].OgAssetID)
	}
	return s.loadReadyManageOgAssetRefs(ctx, candidates...)
}

// workResponseWithReadyOg adds the content projection and the ready OG
// image to view.
func (s *Service) workResponseWithReadyOg(ctx context.Context, view View, imageAsset *mediaasset.Reference) (View, error) {
	if err := s.hydrateWorkContentProjection(ctx, &view); err != nil {
		return View{}, err
	}
	ogAsset, err := s.readyManageOgAssetRef(ctx, view.OgAssetID)
	if err != nil {
		return View{}, err
	}
	view.OgAsset, view.FeaturedImage = ogAsset, imageAsset
	return view, nil
}

// workContent is the content projection a manage read attaches to a Work.
type workContent struct {
	document   *contentv1.RichTextDocument
	blockMedia []*contentv1.ContentBlockMediaItem
	revision   string
}

// hydrateWorkContentProjection reads the Work's source-locale document and
// its block media into view, after the principal's fresh View check.
func (s *Service) hydrateWorkContentProjection(ctx context.Context, view *View) error {
	var snapshot contentblock.Snapshot
	var mediaItems []*contentv1.ContentBlockMediaItem
	err := s.units.ReadLocked(ctx, func(store Store) error {
		if _, err := requireLockedWorkPermission(
			ctx, store, freshRequire(ctx, store, s.fence, s.spiceDB), view.ID, policyv1.Work.View, workAuthorizationRead,
		); err != nil {
			return err
		}
		principal := auth.GetUser(ctx)
		documentID, err := loadWorkContentDocumentID(ctx, store, view.ID, ShareLock)
		if err != nil {
			return err
		}
		source, err := LoadRequiredSourceLocaleMetadata(ctx, store, view.ID)
		if err != nil {
			return err
		}
		if snapshot, err = store.LockedContentSnapshot(ctx, documentID, source.Locale); err != nil {
			return err
		}
		if mediaItems, err = store.MediaReferences(ctx, documentID); err != nil {
			return err
		}
		mediaItems, err = store.HydrateBlockMedia(ctx, view.ID, documentID, principal, mediaItems)
		return err
	})
	if err != nil {
		return normalizeWorkContentBlockError(err)
	}
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return normalizeWorkContentBlockError(err)
	}
	content := workContent{document: document, blockMedia: mediaItems, revision: snapshot.Document.Revision.String()}
	view.Document, view.BlockMedia, view.Revision = content.document, content.blockMedia, content.revision
	return nil
}

// LoadLocalizedWorkContentProjectionForPublic reads the Work's content
// document in locale (the source locale when empty) and its block media,
// through store bound to the public read's snapshot.
func LoadLocalizedWorkContentProjectionForPublic(
	ctx context.Context,
	store Store,
	workID string,
	documentID uuid.UUID,
	locale string,
) (*contentv1.LocalizedRichTextDocument, string, []*contentv1.ContentBlockMediaItem, error) {
	if store == nil {
		return nil, "", nil, errors.New("Work content projection dependencies are required")
	}
	source, err := LoadRequiredSourceLocaleMetadata(ctx, store, workID)
	if err != nil {
		return nil, "", nil, err
	}
	if strings.TrimSpace(locale) == "" {
		locale = source.Locale
	}
	if documentID == uuid.Nil {
		return nil, "", nil, domainerr.Precondition("Work content document is not initialized")
	}
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, source.Locale)
	if err != nil {
		return nil, "", nil, normalizeWorkContentBlockError(err)
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
	if err != nil {
		return nil, "", nil, normalizeWorkContentBlockError(err)
	}
	media, err := store.MediaReferences(ctx, documentID)
	if err != nil {
		return nil, "", nil, err
	}
	return document, snapshot.Document.Revision.String(), media, nil
}

func (s *Service) normalizeMapPlaceID(ctx context.Context, raw string) (*string, error) {
	mapPlaceID := strings.TrimSpace(raw)
	if mapPlaceID == "" {
		return nil, nil
	}

	var exists bool
	if err := s.read(ctx, func(store Store) error {
		var err error
		exists, err = store.MapPlaceExists(ctx, mapPlaceID)
		return err
	}); err != nil {
		return nil, err
	}
	if !exists {
		return nil, domainerr.InvalidField("map_place_id", "map place does not exist")
	}

	return &mapPlaceID, nil
}
