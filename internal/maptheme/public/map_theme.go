package public

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
)

// Reads opens the read snapshots of the Map Theme store.
type Reads interface {
	Read(ctx context.Context, read func(maptheme.Store) error) error
}

// Deps are the collaborators of the public Map Theme reads.
type Deps struct {
	Units Reads // mapthemepostgres.NewUnits(db)
}

// Service holds the public Map Theme reads.
type Service struct {
	units Reads
}

// NewService validates deps and returns the public Map Theme reads.
func NewService(deps Deps) (*Service, error) {
	if deps.Units == nil {
		return nil, errors.New("public map theme dependencies are required: Units")
	}
	return &Service{units: deps.Units}, nil
}

// ResolveByIDsQuery names themes to resolve; an empty ID is skipped.
type ResolveByIDsQuery struct {
	ThemeIDs []string
}

// ResolveQuery names a theme to resolve, falling back to the site default,
// and the color scheme to resolve it in ("light" when empty).
type ResolveQuery struct {
	ThemeID *string
	Scheme  string
}

// Resolution is the theme a requested ID resolved to.
type Resolution struct {
	RequestedID string
	Theme       maptheme.Theme
}

func (s *Service) loadThemes(ctx context.Context, requestedIDs []string) (string, map[string]maptheme.Theme, error) {
	var defaultID string
	var byID map[string]maptheme.Theme
	err := s.units.Read(ctx, func(store maptheme.Store) error {
		var err error
		defaultID, err = store.DefaultThemeID(ctx)
		if err != nil {
			return err
		}
		unique := map[string]struct{}{defaultID: {}}
		for _, id := range requestedIDs {
			if id != "" {
				unique[id] = struct{}{}
			}
		}
		ids := make([]string, 0, len(unique))
		for id := range unique {
			ids = append(ids, id)
		}
		themes, err := store.ThemesByID(ctx, ids)
		if err != nil {
			return err
		}
		byID = make(map[string]maptheme.Theme, len(themes))
		for _, theme := range themes {
			byID[theme.ID] = theme
		}
		if _, ok := byID[defaultID]; !ok {
			return fmt.Errorf("default map theme %s is missing", defaultID)
		}
		return nil
	})
	return defaultID, byID, err
}

// ResolveByIDs resolves each requested theme, falling back to the site
// default for a missing one.
func (s *Service) ResolveByIDs(ctx context.Context, query ResolveByIDsQuery) ([]Resolution, error) {
	requested := make([]string, 0, len(query.ThemeIDs))
	for _, id := range query.ThemeIDs {
		if id == "" {
			continue
		}
		if _, err := uuidutil.ParseCanonical(id, "requested_theme_ids"); err != nil {
			return nil, domainerr.InvalidField("requested_theme_ids", "must contain only canonical UUIDs")
		}
		requested = append(requested, id)
	}
	if len(requested) == 0 {
		return []Resolution{}, nil
	}
	defaultID, themes, err := s.loadThemes(ctx, requested)
	if err != nil {
		return nil, fmt.Errorf("resolve map themes: %w", err)
	}
	results := make([]Resolution, 0, len(requested))
	for _, requestedID := range requested {
		resolved, ok := themes[requestedID]
		if !ok {
			resolved = themes[defaultID]
		}
		results = append(results, Resolution{RequestedID: requestedID, Theme: resolved})
	}
	return results, nil
}

func (s *Service) Resolve(ctx context.Context, query ResolveQuery) (maptheme.ResolvedTheme, error) {
	scheme := query.Scheme
	if scheme == "" {
		scheme = "light"
	}
	if scheme != "light" && scheme != "dark" {
		return maptheme.ResolvedTheme{}, domainerr.InvalidField("scheme", "must be 'light' or 'dark'")
	}
	requestedID := ""
	if query.ThemeID != nil {
		requestedID = *query.ThemeID
		if requestedID != "" {
			if _, err := uuidutil.ParseCanonical(requestedID, "theme_id"); err != nil {
				return maptheme.ResolvedTheme{}, domainerr.InvalidField("theme_id", "must be a canonical UUID")
			}
		}
	}
	defaultID, themes, err := s.loadThemes(ctx, []string{requestedID})
	if err != nil {
		return maptheme.ResolvedTheme{}, fmt.Errorf("resolve map theme: %w", err)
	}
	theme, ok := themes[requestedID]
	if !ok {
		theme = themes[defaultID]
	}
	return theme.Resolve(scheme), nil
}
