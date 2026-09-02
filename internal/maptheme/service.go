package maptheme

import (
	"context"
	"database/sql"
	stderrors "errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type MapThemeService struct {
	managev1connect.UnimplementedMapThemeServiceHandler
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	auditWriter domainaudit.Appender
}

func NewMapThemeService(db *gorm.DB, spiceDB *auth.SpiceDBClient) *MapThemeService {
	dependencycheck.MustNotNil(db, "db")
	dependencycheck.MustNotNil(spiceDB, "spiceDB")
	return &MapThemeService{db: db, spiceDB: spiceDB}
}

func NewAuditedMapThemeService(
	db *gorm.DB, auditWriter domainaudit.Appender, spiceDB *auth.SpiceDBClient,
) *MapThemeService {
	if auditWriter == nil {
		panic("map theme audit writer is required")
	}
	service := NewMapThemeService(db, spiceDB)
	service.auditWriter = auditWriter
	return service
}

func (s *MapThemeService) appendMapThemeCreatedAudit(ctx context.Context, tx *gorm.DB, themeID string) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(
		ctx, tx, s.auditWriter, sharedtelemetry.AuditMapThemeCreated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMapThemeCreatedAuditRecord(metadata, themeID)
		},
	)
}

func (s *MapThemeService) appendMapThemeDeletedAudit(ctx context.Context, tx *gorm.DB, themeID string) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(
		ctx, tx, s.auditWriter, sharedtelemetry.AuditMapThemeDeleted,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMapThemeDeletedAuditRecord(metadata, themeID)
		},
	)
}

func appendMapThemeSiteSettingsAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
) error {
	if writer == nil {
		return nil
	}
	return domainaudit.AppendRequest(
		ctx,
		tx,
		writer,
		sharedtelemetry.AuditSiteSettingsUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewSiteSettingsUpdatedAuditRecord(
				metadata,
				[]string{"default_map_theme_id"},
			)
		},
	)
}

func (s *MapThemeService) requireMapThemeCan(ctx context.Context, can policyv1.Can) error {
	user := auth.GetUser(ctx)
	if user == nil || user.Banned {
		return errs.AdminRequired()
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return err
	}
	return nil
}

// requireLockedMapThemeCan is the final Map Theme mutation fence. The caller
// passes the same generated business action used for initial admission so the
// locked recheck cannot silently fall back to a generic platform role.
func (s *MapThemeService) requireLockedMapThemeCan(
	ctx context.Context,
	tx *gorm.DB,
	can policyv1.Can,
) error {
	return identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can)
}

func lockMapThemeRoot(ctx context.Context, tx *gorm.DB, themeID string) (*model.MapTheme, error) {
	var theme model.MapTheme
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", themeID).
		Take(&theme).Error; err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("theme", themeID)
		}
		return nil, err
	}
	return &theme, nil
}

func manageVariantInput(input *managev1.MapThemeVariantInput) *mapThemeVariantSnapshot {
	if input == nil {
		return nil
	}
	return &mapThemeVariantSnapshot{
		BackgroundColor: input.BackgroundColor, WaterColor: input.WaterColor, LandColor: input.LandColor,
		RoadColor: input.RoadColor, BuildingFillColor: input.BuildingFillColor,
		BuildingStrokeEnabled: input.BuildingStrokeEnabled, BuildingStrokeColor: input.BuildingStrokeColor,
		CalloutLineColor: input.CalloutLineColor, CalloutTextColor: input.CalloutTextColor,
		CalloutBackgroundColor: input.CalloutBackgroundColor, CalloutDescriptionColor: input.CalloutDescriptionColor,
		AttributionColor: input.AttributionColor, LabelTextColor: input.LabelTextColor,
		ClusterColor: input.ClusterColor, ClusterHoverColor: input.ClusterHoverColor,
		ClusterTextColor: input.ClusterTextColor, ClusterTextHoverColor: input.ClusterTextHoverColor,
		CalloutHoverLineColor: input.CalloutHoverLineColor, CalloutHoverTextColor: input.CalloutHoverTextColor,
		CalloutHoverDescriptionColor: input.CalloutHoverDescriptionColor,
		CalloutHoverBackgroundColor:  input.CalloutHoverBackgroundColor,
	}
}

func manageCreateSnapshot(req *managev1.CreateMapThemeRequest) (*mapThemeSnapshot, error) {
	if req.Settings == nil {
		return nil, errs.Required("settings")
	}
	light := manageVariantInput(req.LightVariant)
	dark := manageVariantInput(req.DarkVariant)
	if light == nil {
		return nil, errs.Required("light_variant")
	}
	if dark == nil {
		return nil, errs.Required("dark_variant")
	}
	snapshot := &mapThemeSnapshot{
		Name: req.Name,
		Settings: mapThemeSettingsSnapshot{
			CalloutScale: req.Settings.CalloutScale, CalloutOffsetX: req.Settings.CalloutOffsetX,
			CalloutOffsetY: req.Settings.CalloutOffsetY, CalloutFields: append([]string(nil), req.Settings.CalloutFields...),
			AttributionFontSize: req.Settings.AttributionFontSize, ShowAreaLabels: req.Settings.ShowAreaLabels,
			ShowPoiLabels: req.Settings.ShowPoiLabels,
		},
		LightVariant: *light, DarkVariant: *dark,
	}
	return snapshot, validateMapThemeSnapshot(snapshot)
}

func (s *MapThemeService) variantToProto(themeID, scheme string, v *model.MapThemeVariant) *managev1.MapThemeVariant {
	proto := &managev1.MapThemeVariant{
		// The transport keeps a stable conceptual variant id for clients, but
		// variants are not independently persisted rows.
		Id: themeID + ":" + scheme, BackgroundColor: v.BackgroundColor, WaterColor: v.WaterColor,
		LandColor: v.LandColor, RoadColor: v.RoadColor, BuildingFillColor: v.BuildingFillColor,
		BuildingStrokeEnabled: v.BuildingStrokeEnabled, BuildingStrokeColor: v.BuildingStrokeColor,
		CalloutLineColor: v.CalloutLineColor, CalloutTextColor: v.CalloutTextColor,
		CalloutBackgroundColor: v.CalloutBackgroundColor, CalloutDescriptionColor: v.CalloutDescriptionColor,
		AttributionColor: v.AttributionColor, LabelTextColor: v.LabelTextColor,
		ClusterColor: v.ClusterColor, ClusterHoverColor: v.ClusterHoverColor,
		ClusterTextColor: v.ClusterTextColor, ClusterTextHoverColor: v.ClusterTextHoverColor,
		CalloutHoverLineColor: v.CalloutHoverLineColor, CalloutHoverTextColor: v.CalloutHoverTextColor,
		CalloutHoverDescriptionColor: v.CalloutHoverDescriptionColor,
		CalloutHoverBackgroundColor:  v.CalloutHoverBackgroundColor,
	}
	return proto
}

func (s *MapThemeService) toProto(theme *model.MapTheme) (*managev1.MapTheme, error) {
	proto := &managev1.MapTheme{
		Id: theme.ID, Name: theme.Name, Revision: theme.EditVersion,
		CreatedAt: timestamppb.New(theme.CreatedAt), UpdatedAt: timestamppb.New(theme.UpdatedAt),
		Settings: &managev1.MapThemeSettings{
			CalloutScale: float64(theme.CalloutScale), CalloutOffsetX: int32(theme.CalloutOffsetX),
			CalloutOffsetY: int32(theme.CalloutOffsetY), CalloutFields: theme.CalloutFields,
			AttributionFontSize: int32(theme.AttributionFontSize), ShowAreaLabels: theme.ShowAreaLabels,
			ShowPoiLabels: theme.ShowPoiLabels,
		},
		LightVariant: s.variantToProto(theme.ID, "light", &theme.LightVariant),
		DarkVariant:  s.variantToProto(theme.ID, "dark", &theme.DarkVariant),
	}
	return proto, nil
}

func loadDefaultMapThemeID(ctx context.Context, db *gorm.DB) (string, error) {
	var settings model.SiteSettings
	if err := db.WithContext(ctx).Select("default_map_theme_id").Where("id = ?", 1).Take(&settings).Error; err != nil {
		return "", err
	}
	if strings.TrimSpace(settings.DefaultMapThemeID) == "" {
		return "", fmt.Errorf("site settings default map theme is empty")
	}
	return settings.DefaultMapThemeID, nil
}

func loadMapThemeSnapshotByID(ctx context.Context, db *gorm.DB, id string) (*model.MapTheme, error) {
	var theme model.MapTheme
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Where("id = ?", id).Take(&theme).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	return &theme, nil
}

func loadResolvedMapTheme(ctx context.Context, db *gorm.DB, requestedID string) (*model.MapTheme, error) {
	var resolved model.MapTheme
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		defaultID, err := loadDefaultMapThemeID(ctx, tx)
		if err != nil {
			return err
		}
		ids := []string{defaultID}
		if requestedID != "" && requestedID != defaultID {
			ids = append(ids, requestedID)
		}
		var themes []model.MapTheme
		if err := tx.Where("id IN ?", ids).Find(&themes).Error; err != nil {
			return err
		}
		var fallback *model.MapTheme
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
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	return &resolved, nil
}
