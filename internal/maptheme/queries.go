package maptheme

import (
	"context"
	stderrors "errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// ThemeList is every Map Theme and the site default.
type ThemeList struct {
	Themes    []Theme
	DefaultID string
}

// ResolveMapThemeQuery names a theme to resolve, falling back to the site
// default, and the color scheme to resolve it in ("light" when empty).
type ResolveMapThemeQuery struct {
	ThemeID *string
	Scheme  string
}

// GetMapThemeQuery names a theme.
type GetMapThemeQuery struct {
	ID string
}

// CreateMapThemeCommand creates a theme from a complete snapshot.
type CreateMapThemeCommand struct {
	Snapshot SnapshotInput
}

// DeleteMapThemeCommand deletes a theme that is not the site default.
type DeleteMapThemeCommand struct {
	ID string
}

// CopyMapThemeCommand copies a theme under a new name.
type CopyMapThemeCommand struct {
	ID   string
	Name string
}

// SetDefaultMapThemeCommand makes a theme the site default.
type SetDefaultMapThemeCommand struct {
	ThemeID string
}

func (s *Service) ListMapThemes(ctx context.Context) (ThemeList, error) {
	var defaultID string
	var themes []Theme
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		if defaultID, err = store.DefaultThemeID(ctx); err != nil {
			return err
		}
		themes, err = store.Themes(ctx)
		return err
	})
	if err != nil {
		return ThemeList{}, err
	}
	defaultIncluded := false
	for i := range themes {
		defaultIncluded = defaultIncluded || themes[i].ID == defaultID
	}
	if !defaultIncluded {
		return ThemeList{}, fmt.Errorf("default map theme %s is missing", defaultID)
	}
	return ThemeList{Themes: themes, DefaultID: defaultID}, nil
}

func (s *Service) ResolveMapTheme(ctx context.Context, query ResolveMapThemeQuery) (ResolvedTheme, error) {
	scheme := query.Scheme
	if scheme == "" {
		scheme = "light"
	}
	if scheme != "light" && scheme != "dark" {
		return ResolvedTheme{}, domainerr.InvalidField("scheme", "must be 'light' or 'dark'")
	}
	requestedID := ""
	if query.ThemeID != nil && *query.ThemeID != "" {
		var err error
		if requestedID, err = normalizeMapThemeID(*query.ThemeID, "theme_id"); err != nil {
			return ResolvedTheme{}, err
		}
	}
	theme, err := s.resolvedTheme(ctx, requestedID)
	if err != nil {
		return ResolvedTheme{}, fmt.Errorf("resolve map theme: %w", err)
	}
	return theme.Resolve(scheme), nil
}

func (s *Service) GetMapTheme(ctx context.Context, query GetMapThemeQuery) (Theme, error) {
	id, err := normalizeMapThemeID(query.ID, "id")
	if err != nil {
		return Theme{}, err
	}
	can, err := policyv1.MapTheme.View(id)
	if err != nil {
		return Theme{}, domainerr.InvalidField("id", err.Error())
	}
	if user := auth.GetUser(ctx); user == nil || user.Banned {
		return Theme{}, domainerr.Denied("admin access required")
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return Theme{}, err
	}
	theme, err := s.readTheme(ctx, id)
	if stderrors.Is(err, ErrThemeNotFound) {
		return Theme{}, domainerr.ResourceNotFound("theme", id)
	}
	return theme, err
}

// createdTheme is the mutation spec shared by create and copy: lock takes
// the source row, build makes the theme, and the theme's policy relationship
// is written with it.
func (s *Service) createdTheme(
	ctx context.Context, lock func(context.Context, Store) error, build func(context.Context, Store) (Theme, error),
) (Theme, error) {
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[Theme, Store]{
		Lock:   lock,
		Access: mutation.FreshAdminCan(policyv1.MapTheme.Create()),
		Change: build,
		Audit: func(theme Theme) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMapThemeCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMapThemeCreatedAuditRecord(metadata, theme.ID)
			})
		},
		Relationships: func(theme Theme) (apply, compensate []policyv1.RelationshipMutation, err error) {
			return policyRelationships(theme.ID, true)
		},
	})
}

// policyRelationships is the policy write of a created (touch) or deleted
// theme, and its exact inverse.
func policyRelationships(themeID string, created bool) (apply, compensate []policyv1.RelationshipMutation, err error) {
	touch, err := policyv1.MapTheme.TouchPolicy(themeID)
	if err != nil {
		return nil, nil, err
	}
	remove, err := policyv1.MapTheme.DeletePolicy(themeID)
	if err != nil {
		return nil, nil, err
	}
	if created {
		return []policyv1.RelationshipMutation{touch}, []policyv1.RelationshipMutation{remove}, nil
	}
	return []policyv1.RelationshipMutation{remove}, []policyv1.RelationshipMutation{touch}, nil
}

func (s *Service) CreateMapTheme(ctx context.Context, command CreateMapThemeCommand) (Theme, error) {
	snapshot, err := command.Snapshot.snapshot("")
	if err != nil {
		return Theme{}, err
	}
	return s.createdTheme(ctx, nil, func(ctx context.Context, store Store) (Theme, error) {
		return store.InsertTheme(ctx, *snapshot)
	})
}

func (s *Service) DeleteMapTheme(ctx context.Context, command DeleteMapThemeCommand) error {
	id, err := normalizeMapThemeID(command.ID, "id")
	if err != nil {
		return err
	}
	can, err := policyv1.MapTheme.Delete(id)
	if err != nil {
		return domainerr.InvalidField("id", err.Error())
	}
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Lock: func(ctx context.Context, store Store) error {
			_, err := lockThemeRoot(ctx, store, id)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (string, error) {
			err := store.DeleteTheme(ctx, id)
			if stderrors.Is(err, ErrThemeNotFound) {
				return "", domainerr.ResourceNotFound("theme", id)
			}
			return id, err
		},
		Audit: func(id string) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMapThemeDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMapThemeDeletedAuditRecord(metadata, id)
			})
		},
		Relationships: func(id string) (apply, compensate []policyv1.RelationshipMutation, err error) {
			return policyRelationships(id, false)
		},
	})
	if stderrors.Is(err, ErrDefaultTheme) {
		return domainerr.Precondition("the current default or last map theme cannot be deleted; select another default first").WithCause(err)
	}
	return err
}

func (s *Service) CopyMapTheme(ctx context.Context, command CopyMapThemeCommand) (Theme, error) {
	id, err := normalizeMapThemeID(command.ID, "id")
	if err != nil {
		return Theme{}, err
	}
	name, err := normalizeMapThemeName(command.Name)
	if err != nil {
		return Theme{}, err
	}
	var source Theme
	lock := func(ctx context.Context, store Store) (err error) {
		source, err = lockThemeRoot(ctx, store, id)
		return err
	}
	return s.createdTheme(ctx, lock, func(ctx context.Context, store Store) (Theme, error) {
		snapshot := source.Snapshot
		snapshot.Name = name
		snapshot.Settings.CalloutFields = append([]string(nil), source.Snapshot.Settings.CalloutFields...)
		if err := validateMapThemeSnapshot(&snapshot); err != nil {
			return Theme{}, fmt.Errorf("stored source map theme is invalid: %w", err)
		}
		return store.InsertTheme(ctx, snapshot)
	})
}

func (s *Service) SetDefaultMapTheme(ctx context.Context, command SetDefaultMapThemeCommand) (string, error) {
	themeID, err := normalizeMapThemeID(command.ThemeID, "theme_id")
	if err != nil {
		return "", err
	}
	can, err := policyv1.MapTheme.Manage(themeID)
	if err != nil {
		return "", domainerr.InvalidField("theme_id", err.Error())
	}
	var currentDefaultID string
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[bool, Store]{
		Lock: func(ctx context.Context, store Store) (err error) {
			if currentDefaultID, err = store.LockDefaultThemeID(ctx); err != nil {
				return err
			}
			_, err = lockThemeRoot(ctx, store, themeID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (bool, error) {
			if currentDefaultID == themeID {
				return false, nil
			}
			return true, store.SetDefaultThemeID(ctx, themeID)
		},
		Audit: func(changed bool) mutation.Audit {
			if !changed {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditSiteSettingsUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewSiteSettingsUpdatedAuditRecord(metadata, []string{"default_map_theme_id"})
			})
		},
	})
	if err != nil {
		return "", err
	}
	return themeID, nil
}
