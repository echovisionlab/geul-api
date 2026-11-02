package label

import (
	"context"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/pathslug"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// View is a Label as the manage API renders it: the row, its source-locale
// name and content document, and its ready logos and OG image.
type View struct {
	Label     Label
	Name      string
	Document  *contentv1.RichTextDocument
	Revision  string
	LightLogo *mediaasset.Reference
	DarkLogo  *mediaasset.Reference
	OgImage   *mediaasset.Reference
}

// CheckLabelSlugAvailableQuery asks whether Slug is free for a new Label, or
// for the Label ExcludeLabelID when it is set.
type CheckLabelSlugAvailableQuery struct {
	Slug           string
	ExcludeLabelID *string
}

// CheckLabelSlugAvailable reports whether no other Label and no route use
// the slug. Checking for an existing Label requires Edit on it; for a new
// one, the platform-wide create action.
func (s *Service) CheckLabelSlugAvailable(ctx context.Context, query CheckLabelSlugAvailableQuery) (bool, error) {
	if auth.GetUser(ctx) == nil {
		return false, errAuthenticationRequired
	}
	if query.ExcludeLabelID != nil {
		if err := labelEntity.RequirePermission(ctx, s.spiceDB, *query.ExcludeLabelID, policyv1.Label.Edit); err != nil {
			return false, err
		}
	} else if err := labelEntity.RequireCreate(ctx, s.spiceDB); err != nil {
		return false, err
	}
	if err := pathslug.Validate(query.Slug); err != nil {
		return false, err
	}
	excludeID := ""
	if query.ExcludeLabelID != nil {
		excludeID = *query.ExcludeLabelID
	}
	var available bool
	err := s.units.Read(ctx, func(store Store) error {
		taken, err := store.SlugTaken(ctx, query.Slug, excludeID)
		if err != nil || taken {
			return err
		}
		available, err = store.ResourceRouteAvailable(ctx, query.Slug)
		return err
	})
	return available, err
}

type labelImageAssets struct {
	Light *mediaasset.Reference
	Dark  *mediaasset.Reference
}

func (s *Service) loadReadyLabelLogoAssets(ctx context.Context, labels []Label) (map[string]*mediaasset.Reference, error) {
	fileIDs := make([]string, 0, len(labels)*2)
	seen := make(map[string]struct{}, len(labels)*2)
	for _, label := range labels {
		for _, candidate := range []*string{label.LogoLightFileID, label.LogoDarkFileID} {
			if candidate == nil || strings.TrimSpace(*candidate) == "" {
				continue
			}
			if _, ok := seen[*candidate]; ok {
				continue
			}
			seen[*candidate] = struct{}{}
			fileIDs = append(fileIDs, *candidate)
		}
	}
	refs := make(map[string]*mediaasset.Reference, len(fileIDs))
	if len(fileIDs) == 0 {
		return refs, nil
	}
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		refs, err = store.ReadyLogoAssets(ctx, fileIDs)
		return err
	})
	return refs, err
}

func labelImageAssetsFromReadyMap(label *Label, refs map[string]*mediaasset.Reference) *labelImageAssets {
	result := &labelImageAssets{}
	if label == nil {
		return result
	}
	if label.LogoLightFileID != nil {
		result.Light = refs[*label.LogoLightFileID]
	}
	if label.LogoDarkFileID != nil {
		result.Dark = refs[*label.LogoDarkFileID]
	}
	return result
}

func (s *Service) getLabelImageAssets(ctx context.Context, labelID string) *labelImageAssets {
	var label Label
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		label, err = store.FindLabelLogos(ctx, labelID)
		return err
	}); err != nil {
		slog.WarnContext(ctx, "Label logo lookup failed; serving the Label without logos", "domain", "label", "operation", "load_label_logo_assets", "label_id", labelID, failurecause.Attr(err))
		return &labelImageAssets{}
	}
	refs, err := s.loadReadyLabelLogoAssets(ctx, []Label{label})
	if err != nil {
		slog.WarnContext(ctx, "Label logo asset resolution failed; serving the Label without logos", "domain", "label", "operation", "resolve_label_logo_assets", "label_id", labelID, failurecause.Attr(err))
		return &labelImageAssets{}
	}
	return labelImageAssetsFromReadyMap(&label, refs)
}

// labelDocument is a Label's source-locale name and content document.
type labelDocument struct {
	Name     string
	Document *contentv1.RichTextDocument
	Revision string
}

// overlayLabelSourceLocaleDocument rereads the Label row into label and
// returns its source-locale document from the same snapshot.
func (s *Service) overlayLabelSourceLocaleDocument(ctx context.Context, label *Label) (labelDocument, error) {
	labelID := label.ID
	projection, err := s.loadCreativeManageContentProjection(ctx, labelID, func(ctx context.Context, store Store) error {
		current, err := store.ShareLockLabel(ctx, labelID)
		if err != nil {
			return err
		}
		*label = current
		return nil
	})
	if err != nil {
		return labelDocument{}, err
	}
	return labelDocument{Name: projection.SourceTitle, Document: projection.Document, Revision: projection.Revision}, nil
}

func (s *Service) overlayLabelSourceLocaleDocuments(ctx context.Context, labels []Label) ([]labelDocument, error) {
	documents := make([]labelDocument, len(labels))
	for i := range labels {
		var err error
		if documents[i], err = s.overlayLabelSourceLocaleDocument(ctx, &labels[i]); err != nil {
			return nil, err
		}
	}
	return documents, nil
}

func (s *Service) loadReadyLabelOgAssets(ctx context.Context, labels []Label) (map[string]*mediaasset.Reference, error) {
	candidates := make([]*string, 0, len(labels))
	for i := range labels {
		candidates = append(candidates, labelOgAssetIDForResponse(&labels[i]))
	}
	return s.loadReadyManageOgAssetRefs(ctx, candidates...)
}

func labelOgAssetIDForResponse(label *Label) *string {
	if label == nil || effectiveLabelLogoFileID(label.LogoLightFileID, label.LogoDarkFileID) == nil {
		return nil
	}
	return label.OgAssetID
}

func (s *Service) labelWithReadyOg(ctx context.Context, label Label, document labelDocument, logos *labelImageAssets) (View, error) {
	ogAsset, err := s.readyManageOgAssetRef(ctx, labelOgAssetIDForResponse(&label))
	if err != nil {
		return View{}, err
	}
	return labelView(label, document, logos, ogAsset), nil
}

// labelView combines a Label row with its document and assets. A Label
// without an effective logo shows no OG image.
func labelView(label Label, document labelDocument, logos *labelImageAssets, ogAsset *mediaasset.Reference) View {
	if labelOgAssetIDForResponse(&label) == nil {
		ogAsset = nil
	}
	view := View{Label: label, Name: document.Name, Document: document.Document, Revision: document.Revision, OgImage: ogAsset}
	if logos != nil {
		view.LightLogo, view.DarkLogo = logos.Light, logos.Dark
	}
	return view
}

func (s *Service) loadReadyManageOgAssetRefs(ctx context.Context, candidates ...*string) (map[string]*mediaasset.Reference, error) {
	ids := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		id := strings.TrimSpace(*candidate)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	var ready map[string]*mediaasset.Reference
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		ready, err = store.ReadyAssets(ctx, ids)
		return err
	})
	return ready, err
}

func manageOgAssetFromReadyMap(ready map[string]*mediaasset.Reference, candidates ...*string) *mediaasset.Reference {
	for _, candidate := range candidates {
		if candidate != nil {
			if asset := ready[strings.TrimSpace(*candidate)]; asset != nil {
				return asset
			}
		}
	}
	return nil
}

func (s *Service) readyManageOgAssetRef(ctx context.Context, candidates ...*string) (*mediaasset.Reference, error) {
	ready, err := s.loadReadyManageOgAssetRefs(ctx, candidates...)
	if err != nil {
		return nil, err
	}
	return manageOgAssetFromReadyMap(ready, candidates...), nil
}

// checkSlugAvailable validates slug and requires that no other Label
// holds it; excludeID is the Label being updated, empty for a new one.
func checkSlugAvailable(ctx context.Context, store Store, slug string, excludeID string) error {
	if err := pathslug.Validate(slug); err != nil {
		return err
	}
	if slug == "" {
		return nil
	}
	taken, err := store.SlugTaken(ctx, slug, excludeID)
	if err != nil {
		return err
	}
	if taken {
		return domainerr.Duplicate("label", "slug", slug)
	}
	return nil
}
