package maptheme

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// ThemeList is every Map Theme and the site default.
type ThemeList struct {
	Themes    []*managev1.MapTheme
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
	var themes []model.MapTheme
	err := mutation.Read(ctx, s.db, func(tx *gorm.DB) error {
		var err error
		defaultID, err = loadDefaultMapThemeID(ctx, tx)
		if err != nil {
			return err
		}
		return tx.Order("name ASC, id ASC").Find(&themes).Error
	})
	if err != nil {
		return ThemeList{}, err
	}
	result := make([]*managev1.MapTheme, 0, len(themes))
	defaultIncluded := false
	for i := range themes {
		defaultIncluded = defaultIncluded || themes[i].ID == defaultID
		result = append(result, toProto(&themes[i]))
	}
	if !defaultIncluded {
		return ThemeList{}, fmt.Errorf("default map theme %s is missing", defaultID)
	}
	return ThemeList{Themes: result, DefaultID: defaultID}, nil
}

func (s *Service) ResolveMapTheme(ctx context.Context, query ResolveMapThemeQuery) (*managev1.ResolvedMapTheme, error) {
	scheme := query.Scheme
	if scheme == "" {
		scheme = "light"
	}
	if scheme != "light" && scheme != "dark" {
		return nil, domainerr.InvalidField("scheme", "must be 'light' or 'dark'")
	}
	requestedID := ""
	if query.ThemeID != nil && *query.ThemeID != "" {
		var err error
		if requestedID, err = normalizeMapThemeID(*query.ThemeID, "theme_id"); err != nil {
			return nil, err
		}
	}
	theme, err := loadResolvedMapTheme(ctx, s.db, requestedID)
	if err != nil {
		return nil, fmt.Errorf("resolve map theme: %w", err)
	}
	variant := &theme.LightVariant
	if scheme == "dark" {
		variant = &theme.DarkVariant
	}
	return &managev1.ResolvedMapTheme{
		ThemeId: theme.ID, Scheme: scheme, Settings: settingsToProto(theme),
		Variant: variantToProto(theme.ID, scheme, variant),
	}, nil
}

func (s *Service) GetMapTheme(ctx context.Context, query GetMapThemeQuery) (*managev1.MapTheme, error) {
	id, err := normalizeMapThemeID(query.ID, "id")
	if err != nil {
		return nil, err
	}
	can, err := policyv1.MapTheme.View(id)
	if err != nil {
		return nil, domainerr.InvalidField("id", err.Error())
	}
	if user := auth.GetUser(ctx); user == nil || user.Banned {
		return nil, domainerr.Denied("admin access required")
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}
	theme, err := loadMapThemeSnapshotByID(ctx, s.db, id)
	if err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("theme", id)
		}
		return nil, err
	}
	return toProto(theme), nil
}

func createMapThemeFromSnapshot(tx *gorm.DB, snapshot Snapshot) (*model.MapTheme, error) {
	theme := &model.MapTheme{
		Name: snapshot.Name, EditVersion: 1,
		CalloutScale: float32(snapshot.Settings.CalloutScale), CalloutOffsetX: int(snapshot.Settings.CalloutOffsetX),
		CalloutOffsetY:      int(snapshot.Settings.CalloutOffsetY),
		CalloutFields:       append([]string(nil), snapshot.Settings.CalloutFields...),
		AttributionFontSize: int(snapshot.Settings.AttributionFontSize), ShowAreaLabels: snapshot.Settings.ShowAreaLabels,
		ShowPoiLabels: snapshot.Settings.ShowPoiLabels,
		LightVariant:  mapThemeVariantModel(snapshot.LightVariant),
		DarkVariant:   mapThemeVariantModel(snapshot.DarkVariant),
	}
	if err := tx.Clauses(clause.Returning{}).Create(theme).Error; err != nil {
		return nil, err
	}
	return theme, nil
}

// createdTheme is the mutation spec shared by create and copy: lock takes
// the source row, build makes the theme, and the theme's policy relationship
// is written with it.
func (s *Service) createdTheme(
	ctx context.Context, lock func(context.Context, *gorm.DB) error, build func(context.Context, *gorm.DB) (*model.MapTheme, error),
) (*managev1.MapTheme, error) {
	theme, err := mutation.Run(ctx, s.mutations, mutation.Spec[*model.MapTheme]{
		Lock:   lock,
		Access: mutation.FreshAdminCan(policyv1.MapTheme.Create()),
		Change: build,
		Audit: func(theme *model.MapTheme) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMapThemeCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMapThemeCreatedAuditRecord(metadata, theme.ID)
			})
		},
		Relationships: func(theme *model.MapTheme) (apply, compensate []policyv1.RelationshipMutation, err error) {
			return policyRelationships(theme.ID, true)
		},
	})
	if err != nil {
		return nil, err
	}
	return toProto(theme), nil
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

func (s *Service) CreateMapTheme(ctx context.Context, command CreateMapThemeCommand) (*managev1.MapTheme, error) {
	snapshot, err := command.Snapshot.snapshot("")
	if err != nil {
		return nil, err
	}
	return s.createdTheme(ctx, nil, func(_ context.Context, tx *gorm.DB) (*model.MapTheme, error) {
		return createMapThemeFromSnapshot(tx, *snapshot)
	})
}

func isMapThemeDefaultDeleteViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !stderrors.As(err, &pgErr) || pgErr.ConstraintName != "site_settings_default_map_theme_id_fkey" {
		return false
	}
	return pgErr.Code == "23001" || pgErr.Code == "23503"
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
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			_, err := lockMapThemeRoot(ctx, tx, id)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(_ context.Context, tx *gorm.DB) (string, error) {
			result := tx.Where("id = ?", id).Delete(&model.MapTheme{})
			if result.Error != nil {
				return "", result.Error
			}
			if result.RowsAffected == 0 {
				return "", domainerr.ResourceNotFound("theme", id)
			}
			return id, nil
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
	if isMapThemeDefaultDeleteViolation(err) {
		return domainerr.Precondition("the current default or last map theme cannot be deleted; select another default first").WithCause(err)
	}
	return err
}

func (s *Service) CopyMapTheme(ctx context.Context, command CopyMapThemeCommand) (*managev1.MapTheme, error) {
	id, err := normalizeMapThemeID(command.ID, "id")
	if err != nil {
		return nil, err
	}
	name, err := normalizeMapThemeName(command.Name)
	if err != nil {
		return nil, err
	}
	var source *model.MapTheme
	lock := func(ctx context.Context, tx *gorm.DB) (err error) {
		source, err = lockMapThemeRoot(ctx, tx, id)
		return err
	}
	return s.createdTheme(ctx, lock, func(_ context.Context, tx *gorm.DB) (*model.MapTheme, error) {
		snapshot := Snapshot{
			Name: name, Settings: settingsFromModel(source),
			LightVariant: variantFromModel(source.LightVariant),
			DarkVariant:  variantFromModel(source.DarkVariant),
		}
		if err := validateMapThemeSnapshot(&snapshot); err != nil {
			return nil, fmt.Errorf("stored source map theme is invalid: %w", err)
		}
		return createMapThemeFromSnapshot(tx, snapshot)
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
	var settings model.SiteSettings
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[bool]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&settings, "id = 1").Error; err != nil {
				return err
			}
			_, err := lockMapThemeRoot(ctx, tx, themeID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(_ context.Context, tx *gorm.DB) (bool, error) {
			if settings.DefaultMapThemeID == themeID {
				return false, nil
			}
			return true, tx.Model(settings).Updates(structured.Fields{
				"default_map_theme_id": themeID,
				"updated_at":           time.Now(),
			}).Error
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
