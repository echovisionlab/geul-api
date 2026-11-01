package maptheme

import (
	"context"
	stderrors "errors"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Deps are the collaborators of the Map Theme use cases; all are required.
type Deps struct {
	DB      *gorm.DB
	SpiceDB *auth.SpiceDBClient
	Audit   domainaudit.Appender
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Audit": d.Audit == nil} {
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
	db        *gorm.DB
	spiceDB   *auth.SpiceDBClient
	mutations *mutation.Runner
}

// NewService validates deps and returns the Map Theme use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{db: deps.DB, spiceDB: deps.SpiceDB, mutations: mutations}, nil
}

func lockMapThemeRoot(ctx context.Context, tx *gorm.DB, themeID string) (*model.MapTheme, error) {
	var theme model.MapTheme
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", themeID).
		Take(&theme).Error; err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("theme", themeID)
		}
		return nil, err
	}
	return &theme, nil
}

func variantToProto(themeID, scheme string, v *model.MapThemeVariant) *managev1.MapThemeVariant {
	return &managev1.MapThemeVariant{
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
}

func settingsToProto(theme *model.MapTheme) *managev1.MapThemeSettings {
	return &managev1.MapThemeSettings{
		CalloutScale: float64(theme.CalloutScale), CalloutOffsetX: int32(theme.CalloutOffsetX),
		CalloutOffsetY: int32(theme.CalloutOffsetY), CalloutFields: theme.CalloutFields,
		AttributionFontSize: int32(theme.AttributionFontSize), ShowAreaLabels: theme.ShowAreaLabels,
		ShowPoiLabels: theme.ShowPoiLabels,
	}
}

func toProto(theme *model.MapTheme) *managev1.MapTheme {
	return &managev1.MapTheme{
		Id: theme.ID, Name: theme.Name, Revision: theme.EditVersion,
		CreatedAt: timestamppb.New(theme.CreatedAt), UpdatedAt: timestamppb.New(theme.UpdatedAt),
		Settings:     settingsToProto(theme),
		LightVariant: variantToProto(theme.ID, "light", &theme.LightVariant),
		DarkVariant:  variantToProto(theme.ID, "dark", &theme.DarkVariant),
	}
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
	if err := mutation.Read(ctx, db, func(tx *gorm.DB) error {
		return tx.Where("id = ?", id).Take(&theme).Error
	}); err != nil {
		return nil, err
	}
	return &theme, nil
}

func loadResolvedMapTheme(ctx context.Context, db *gorm.DB, requestedID string) (*model.MapTheme, error) {
	var resolved model.MapTheme
	err := mutation.Read(ctx, db, func(tx *gorm.DB) error {
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
	})
	if err != nil {
		return nil, err
	}
	return &resolved, nil
}
