package form

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// View is a Form as management reads it: the row, its source title and
// schema, and its images.
type View struct {
	Form          Form
	Title         string
	Schema        []byte
	OgAsset       *Asset
	FeaturedImage *Asset
}

func (s *Service) view(ctx context.Context, form Form) (View, error) {
	view := View{Form: form}
	if err := s.units.Read(ctx, func(store Store) error {
		sourceState, err := LoadFormCanonicalSourceDocumentState(ctx, store, form.ID, form.SourceLocale)
		if err != nil {
			return err
		}
		view.Title, view.Schema = resolveFormTitle(sourceState.Title), sourceState.ContentJSON
		view.OgAsset, err = store.ReadyOGAsset(ctx, sourceState.OgAssetID, form.OgAssetID)
		return err
	}); err != nil {
		return View{}, err
	}
	view.FeaturedImage = s.featuredImage(ctx, form.ID)
	return view, nil
}

// featuredImage resolves the Form's featured image in a read of its own: a
// failure is logged and serves the Form without one.
func (s *Service) featuredImage(ctx context.Context, formID string) *Asset {
	var image *Asset
	if err := s.units.Read(ctx, func(store Store) error {
		image = store.FeaturedImage(ctx, formID)
		return nil
	}); err != nil {
		slog.ErrorContext(ctx, "Form featured image lookup failed; serving the Form without a featured image", failurecause.Attr(err), "form_id", formID)
		return nil
	}
	return image
}
