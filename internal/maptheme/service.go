package maptheme

import (
	"context"
	stderrors "errors"
	"fmt"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// Deps are the collaborators of the Map Theme use cases; all are required.
type Deps struct {
	Units   mutation.UnitOfWork[Store] // mapthemepostgres.NewUnits(db)
	SpiceDB Authorizer
	Fence   identitystate.Fence // identitystate.PostgresFence{}
	Audit   domainaudit.Appender
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return stderrors.New("map theme dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Map Theme use cases of administrators and of the
// collaboration service. Connect and MCP adapters translate their requests
// into its commands and queries.
type Service struct {
	units     mutation.UnitOfWork[Store]
	spiceDB   Authorizer
	mutations *mutation.StoreRunner[Store]
}

// NewService validates deps and returns the Map Theme use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{units: deps.Units, spiceDB: deps.SpiceDB, mutations: mutations}, nil
}

// lockThemeRoot locks the theme a mutation changes or copies; a missing
// theme answers NotFound before authorization.
func lockThemeRoot(ctx context.Context, store Store, themeID string) (Theme, error) {
	theme, err := store.LockTheme(ctx, themeID)
	if stderrors.Is(err, ErrThemeNotFound) {
		return Theme{}, domainerr.ResourceNotFound("theme", themeID)
	}
	return theme, err
}

// readTheme reads one theme in its own snapshot.
func (s *Service) readTheme(ctx context.Context, id string) (Theme, error) {
	var theme Theme
	err := s.units.Read(ctx, func(store Store) (err error) {
		theme, err = store.Theme(ctx, id)
		return err
	})
	return theme, err
}

// resolvedTheme reads the requested theme, or the site default when it is
// empty or missing, in one snapshot.
func (s *Service) resolvedTheme(ctx context.Context, requestedID string) (Theme, error) {
	var resolved Theme
	err := s.units.Read(ctx, func(store Store) error {
		defaultID, err := store.DefaultThemeID(ctx)
		if err != nil {
			return err
		}
		ids := []string{defaultID}
		if requestedID != "" && requestedID != defaultID {
			ids = append(ids, requestedID)
		}
		themes, err := store.ThemesByID(ctx, ids)
		if err != nil {
			return err
		}
		var fallback *Theme
		for i := range themes {
			if themes[i].ID == requestedID {
				resolved = themes[i]
				return nil
			}
			if themes[i].ID == defaultID {
				fallback = &themes[i]
			}
		}
		if fallback == nil {
			return fmt.Errorf("default map theme %s is missing", defaultID)
		}
		resolved = *fallback
		return nil
	})
	return resolved, err
}
