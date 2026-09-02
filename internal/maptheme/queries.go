package maptheme

import (
	"context"
	"database/sql"
	stderrors "errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authzmutation"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func (s *MapThemeService) ListMapThemes(
	ctx context.Context, _ *connect.Request[managev1.ListMapThemesRequest],
) (*connect.Response[managev1.ListMapThemesResponse], error) {
	var defaultID string
	var themes []model.MapTheme
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		defaultID, err = loadDefaultMapThemeID(ctx, tx)
		if err != nil {
			return err
		}
		return tx.Order("name ASC, id ASC").Find(&themes).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, errs.Internal(err)
	}
	result := make([]*managev1.MapTheme, 0, len(themes))
	defaultIncluded := false
	for i := range themes {
		defaultIncluded = defaultIncluded || themes[i].ID == defaultID
		item, err := s.toProto(&themes[i])
		if err != nil {
			return nil, errs.Internal(err)
		}
		result = append(result, item)
	}
	if !defaultIncluded {
		return nil, errs.Internal(fmt.Errorf("default map theme %s is missing", defaultID))
	}
	return connect.NewResponse(&managev1.ListMapThemesResponse{Themes: result, DefaultMapThemeId: defaultID}), nil
}

func (s *MapThemeService) ResolveMapTheme(
	ctx context.Context, req *connect.Request[managev1.ResolveMapThemeRequest],
) (*connect.Response[managev1.ResolvedMapTheme], error) {
	scheme := req.Msg.Scheme
	if scheme == "" {
		scheme = "light"
	}
	if scheme != "light" && scheme != "dark" {
		return nil, errs.InvalidArgument("scheme", "must be 'light' or 'dark'")
	}
	requestedID := ""
	if req.Msg.ThemeId != nil {
		var err error
		requestedID = *req.Msg.ThemeId
		if requestedID != "" {
			requestedID, err = normalizeMapThemeID(requestedID, "theme_id")
			if err != nil {
				return nil, err
			}
		}
	}
	theme, err := loadResolvedMapTheme(ctx, s.db, requestedID)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("resolve map theme: %w", err))
	}
	variant := &theme.LightVariant
	if scheme == "dark" {
		variant = &theme.DarkVariant
	}
	return connect.NewResponse(&managev1.ResolvedMapTheme{
		ThemeId: theme.ID, Scheme: scheme,
		Settings: &managev1.MapThemeSettings{
			CalloutScale: float64(theme.CalloutScale), CalloutOffsetX: int32(theme.CalloutOffsetX),
			CalloutOffsetY: int32(theme.CalloutOffsetY), CalloutFields: theme.CalloutFields,
			AttributionFontSize: int32(theme.AttributionFontSize), ShowAreaLabels: theme.ShowAreaLabels,
			ShowPoiLabels: theme.ShowPoiLabels,
		}, Variant: s.variantToProto(theme.ID, scheme, variant),
	}), nil
}

func (s *MapThemeService) GetMapTheme(
	ctx context.Context, req *connect.Request[managev1.GetMapThemeRequest],
) (*connect.Response[managev1.MapTheme], error) {
	id, err := normalizeMapThemeID(req.Msg.Id, "id")
	if err != nil {
		return nil, err
	}
	can, err := policyv1.MapTheme.View(id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}
	if err := s.requireMapThemeCan(ctx, can); err != nil {
		return nil, err
	}
	theme, err := loadMapThemeSnapshotByID(ctx, s.db, id)
	if err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("theme", id)
		}
		return nil, errs.Internal(err)
	}
	result, err := s.toProto(theme)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(result), nil
}

func createMapThemeFromSnapshot(tx *gorm.DB, snapshot mapThemeSnapshot) (*model.MapTheme, error) {
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

func (s *MapThemeService) CreateMapTheme(
	ctx context.Context, req *connect.Request[managev1.CreateMapThemeRequest],
) (*connect.Response[managev1.MapTheme], error) {
	can, err := policyv1.MapTheme.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	snapshot, err := manageCreateSnapshot(req.Msg)
	if err != nil {
		return nil, err
	}
	var theme *model.MapTheme
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := s.requireLockedMapThemeCan(ctx, tx, can); err != nil {
			return err
		}
		var createErr error
		theme, createErr = createMapThemeFromSnapshot(tx, *snapshot)
		if createErr != nil {
			return createErr
		}
		apply, err := policyv1.MapTheme.TouchPolicy(theme.ID)
		if err != nil {
			return err
		}
		compensate, err := policyv1.MapTheme.DeletePolicy(theme.ID)
		if err != nil {
			return err
		}
		if err := write(
			[]policyv1.RelationshipMutation{apply},
			[]policyv1.RelationshipMutation{compensate},
		); err != nil {
			return err
		}
		return s.appendMapThemeCreatedAudit(ctx, tx, theme.ID)
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		slog.Error("failed to create map theme", "error", err)
		return nil, errs.Internal(err)
	}
	result, err := s.toProto(theme)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(result), nil
}

func isMapThemeDefaultDeleteViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !stderrors.As(err, &pgErr) || pgErr.ConstraintName != "site_settings_default_map_theme_id_fkey" {
		return false
	}
	return pgErr.Code == "23001" || pgErr.Code == "23503"
}

func (s *MapThemeService) DeleteMapTheme(
	ctx context.Context, req *connect.Request[managev1.DeleteMapThemeRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	id, err := normalizeMapThemeID(req.Msg.Id, "id")
	if err != nil {
		return nil, err
	}
	can, err := policyv1.MapTheme.Delete(id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}
	var rowsAffected int64
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if _, err := lockMapThemeRoot(ctx, tx, id); err != nil {
			return err
		}
		if err := s.requireLockedMapThemeCan(ctx, tx, can); err != nil {
			return err
		}
		result := tx.Where("id = ?", id).Delete(&model.MapTheme{})
		rowsAffected = result.RowsAffected
		if result.Error != nil {
			return result.Error
		}
		if rowsAffected == 0 {
			return errs.NotFound("theme", id)
		}
		apply, err := policyv1.MapTheme.DeletePolicy(id)
		if err != nil {
			return err
		}
		compensate, err := policyv1.MapTheme.TouchPolicy(id)
		if err != nil {
			return err
		}
		if err := write(
			[]policyv1.RelationshipMutation{apply},
			[]policyv1.RelationshipMutation{compensate},
		); err != nil {
			return err
		}
		return s.appendMapThemeDeletedAudit(ctx, tx, id)
	})
	if err != nil {
		if isMapThemeDefaultDeleteViolation(err) {
			return nil, errs.FailedPrecondition(
				"the current default or last map theme cannot be deleted; select another default first",
			)
		}
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	if rowsAffected == 0 {
		return nil, errs.NotFound("theme", id)
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

func (s *MapThemeService) CopyMapTheme(
	ctx context.Context, req *connect.Request[managev1.CopyMapThemeRequest],
) (*connect.Response[managev1.MapTheme], error) {
	can, err := policyv1.MapTheme.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	id, err := normalizeMapThemeID(req.Msg.Id, "id")
	if err != nil {
		return nil, err
	}
	name, err := normalizeMapThemeName(req.Msg.Name)
	if err != nil {
		return nil, err
	}
	var copied *model.MapTheme
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		source, err := lockMapThemeRoot(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := s.requireLockedMapThemeCan(ctx, tx, can); err != nil {
			return err
		}
		snapshot := mapThemeSnapshot{
			Name: name, Settings: mapThemeSettingsFromModel(source),
			LightVariant: mapThemeVariantSnapshotFromModel(source.LightVariant),
			DarkVariant:  mapThemeVariantSnapshotFromModel(source.DarkVariant),
		}
		if err := validateMapThemeSnapshot(&snapshot); err != nil {
			return fmt.Errorf("stored source map theme is invalid: %w", err)
		}
		var createErr error
		copied, createErr = createMapThemeFromSnapshot(tx, snapshot)
		if createErr != nil {
			return createErr
		}
		apply, err := policyv1.MapTheme.TouchPolicy(copied.ID)
		if err != nil {
			return err
		}
		compensate, err := policyv1.MapTheme.DeletePolicy(copied.ID)
		if err != nil {
			return err
		}
		if err := write(
			[]policyv1.RelationshipMutation{apply},
			[]policyv1.RelationshipMutation{compensate},
		); err != nil {
			return err
		}
		return s.appendMapThemeCreatedAudit(ctx, tx, copied.ID)
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	result, err := s.toProto(copied)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(result), nil
}

func (s *MapThemeService) SetDefaultMapTheme(
	ctx context.Context, req *connect.Request[managev1.SetDefaultMapThemeRequest],
) (*connect.Response[managev1.SetDefaultMapThemeResponse], error) {
	themeID, err := normalizeMapThemeID(req.Msg.ThemeId, "theme_id")
	if err != nil {
		return nil, err
	}
	can, err := policyv1.MapTheme.Manage(themeID)
	if err != nil {
		return nil, errs.InvalidArgument("theme_id", err.Error())
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var settings model.SiteSettings
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&settings, "id = 1").Error; err != nil {
			return err
		}

		if _, err := lockMapThemeRoot(ctx, tx, themeID); err != nil {
			return err
		}
		if err := s.requireLockedMapThemeCan(ctx, tx, can); err != nil {
			return err
		}
		if settings.DefaultMapThemeID == themeID {
			return nil
		}

		if err := tx.Model(settings).Updates(structured.Fields{
			"default_map_theme_id": themeID,
			"updated_at":           time.Now(),
		}).Error; err != nil {
			return err
		}
		return appendMapThemeSiteSettingsAudit(ctx, tx, s.auditWriter)
	})
	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.SetDefaultMapThemeResponse{DefaultMapThemeId: themeID}), nil
}
