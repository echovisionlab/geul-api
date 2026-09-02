package maptheme

import (
	"context"
	stderrors "errors"
	"maps"
	"time"

	"connectrpc.com/connect"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

var errMapThemeRevisionConflict = stderrors.New("map theme revision conflict")

// InternalMapService is the typed durable Map Theme snapshot boundary used by
// Collab. Yjs is materialized only in the Collab process and is never stored.
type InternalMapService struct {
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	auditWriter domainaudit.Appender
}

func NewInternalMapService(db *gorm.DB, spiceDB *auth.SpiceDBClient) *InternalMapService {
	if db == nil || spiceDB == nil {
		panic("internal map theme service requires database and SpiceDB")
	}
	return &InternalMapService{db: db, spiceDB: spiceDB}
}

func NewAuditedInternalMapService(
	db *gorm.DB, auditWriter domainaudit.Appender, spiceDB *auth.SpiceDBClient,
) *InternalMapService {
	if auditWriter == nil {
		panic("internal map theme audit writer is required")
	}
	service := NewInternalMapService(db, spiceDB)
	service.auditWriter = auditWriter
	return service
}

func (s *InternalMapService) appendContentUpdatedAudit(
	ctx context.Context,
	tx *gorm.DB,
	themeID string,
	memberID string,
) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendMember(
		ctx,
		tx,
		s.auditWriter,
		memberID,
		sharedtelemetry.AuditMapThemeUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMapThemeContentUpdatedAuditRecord(metadata, themeID)
		},
	)
}

func internalVariantSnapshot(input *intrav1.MapThemeDocumentVariant) *mapThemeVariantSnapshot {
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

func internalSnapshot(input *intrav1.MapThemeDocumentSnapshot) (*mapThemeSnapshot, error) {
	if input == nil {
		return nil, errs.Required("snapshot")
	}
	if input.Settings == nil {
		return nil, errs.Required("snapshot.settings")
	}
	light := internalVariantSnapshot(input.LightVariant)
	dark := internalVariantSnapshot(input.DarkVariant)
	if light == nil {
		return nil, errs.Required("snapshot.light_variant")
	}
	if dark == nil {
		return nil, errs.Required("snapshot.dark_variant")
	}
	snapshot := &mapThemeSnapshot{
		Name: input.Name,
		Settings: mapThemeSettingsSnapshot{
			CalloutScale: input.Settings.CalloutScale, CalloutOffsetX: input.Settings.CalloutOffsetX,
			CalloutOffsetY:      input.Settings.CalloutOffsetY,
			CalloutFields:       append([]string(nil), input.Settings.CalloutFields...),
			AttributionFontSize: input.Settings.AttributionFontSize,
			ShowAreaLabels:      input.Settings.ShowAreaLabels, ShowPoiLabels: input.Settings.ShowPoiLabels,
		},
		LightVariant: *light, DarkVariant: *dark,
	}
	return snapshot, validateMapThemeSnapshot(snapshot)
}

func internalVariantProto(input model.MapThemeVariant) *intrav1.MapThemeDocumentVariant {
	return &intrav1.MapThemeDocumentVariant{
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

func internalSnapshotProto(theme *model.MapTheme) (*intrav1.MapThemeDocumentSnapshot, error) {
	return &intrav1.MapThemeDocumentSnapshot{
		Name: theme.Name,
		Settings: &intrav1.MapThemeDocumentSettings{
			CalloutScale: float64(theme.CalloutScale), CalloutOffsetX: int32(theme.CalloutOffsetX),
			CalloutOffsetY: int32(theme.CalloutOffsetY), CalloutFields: theme.CalloutFields,
			AttributionFontSize: int32(theme.AttributionFontSize), ShowAreaLabels: theme.ShowAreaLabels,
			ShowPoiLabels: theme.ShowPoiLabels,
		},
		LightVariant: internalVariantProto(theme.LightVariant),
		DarkVariant:  internalVariantProto(theme.DarkVariant),
	}, nil
}

func mapThemeVariantUpdates(prefix string, input mapThemeVariantSnapshot) structured.Fields {
	return structured.Fields{
		prefix + "background_color": input.BackgroundColor, prefix + "water_color": input.WaterColor,
		prefix + "land_color": input.LandColor, prefix + "road_color": input.RoadColor,
		prefix + "building_fill_color":     input.BuildingFillColor,
		prefix + "building_stroke_enabled": input.BuildingStrokeEnabled,
		prefix + "building_stroke_color":   input.BuildingStrokeColor,
		prefix + "callout_line_color":      input.CalloutLineColor, prefix + "callout_text_color": input.CalloutTextColor,
		prefix + "callout_background_color":  input.CalloutBackgroundColor,
		prefix + "callout_description_color": input.CalloutDescriptionColor,
		prefix + "attribution_color":         input.AttributionColor, prefix + "label_text_color": input.LabelTextColor,
		prefix + "cluster_color": input.ClusterColor, prefix + "cluster_hover_color": input.ClusterHoverColor,
		prefix + "cluster_text_color":              input.ClusterTextColor,
		prefix + "cluster_text_hover_color":        input.ClusterTextHoverColor,
		prefix + "callout_hover_line_color":        input.CalloutHoverLineColor,
		prefix + "callout_hover_text_color":        input.CalloutHoverTextColor,
		prefix + "callout_hover_description_color": input.CalloutHoverDescriptionColor,
		prefix + "callout_hover_background_color":  input.CalloutHoverBackgroundColor,
	}
}

func mapThemeSnapshotUpdates(snapshot *mapThemeSnapshot) structured.Fields {
	updates := structured.Fields{
		"name": snapshot.Name, "callout_scale": float32(snapshot.Settings.CalloutScale),
		"callout_offset_x":      int(snapshot.Settings.CalloutOffsetX),
		"callout_offset_y":      int(snapshot.Settings.CalloutOffsetY),
		"callout_fields":        pq.StringArray(snapshot.Settings.CalloutFields),
		"attribution_font_size": int(snapshot.Settings.AttributionFontSize),
		"show_area_labels":      snapshot.Settings.ShowAreaLabels,
		"show_poi_labels":       snapshot.Settings.ShowPoiLabels,
	}
	maps.Copy(updates, mapThemeVariantUpdates("light_", snapshot.LightVariant))
	maps.Copy(updates, mapThemeVariantUpdates("dark_", snapshot.DarkVariant))
	return updates
}

// requireLockedMapThemeContributorsEdit is the final collaboration mutation
// fence. The Collab service is authenticated separately, but its contributor
// IDs are only an attribution claim: each is re-resolved to a canonical live
// Member/Identity while the Map Theme root is locked, then checked against the
// generated Map Theme edit permission with fully-consistent SpiceDB semantics.
func (s *InternalMapService) requireLockedMapThemeContributorsEdit(
	ctx context.Context,
	tx *gorm.DB,
	themeID string,
	memberIDs []string,
) error {
	if len(memberIDs) != 1 {
		return errs.NoPermission("edit", "map theme")
	}
	memberID := memberIDs[0]
	if _, err := uuidutil.ParseCanonical(memberID, "contributor_member_ids"); err != nil {
		return errs.InvalidArgument("contributor_member_ids", "must contain exactly one canonical Member UUID")
	}
	can, err := policyv1.MapTheme.Edit(themeID)
	if err != nil {
		return errs.NotFound("theme", themeID)
	}
	target, err := authorizationtarget.RequireLocked(ctx, tx, memberID)
	if err != nil {
		if connect.CodeOf(err) == connect.CodeInternal {
			return err
		}
		return errs.NoPermission("edit", "map theme")
	}
	actor, err := policyv1.NewAccountIdentityActor(target.IdentityID)
	if err != nil {
		return errs.NoPermission("edit", "map theme")
	}
	allowed, err := s.spiceDB.CheckActorCan(ctx, actor, can)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NoPermission("edit", "map theme")
	}
	return nil
}

func (s *InternalMapService) SaveMapThemeSnapshot(
	ctx context.Context, req *connect.Request[intrav1.SaveMapThemeSnapshotRequest],
) (*connect.Response[intrav1.SaveMapThemeSnapshotResponse], error) {
	themeID, err := normalizeMapThemeID(req.Msg.ThemeId, "theme_id")
	if err != nil {
		return nil, err
	}
	if req.Msg.ExpectedRevision <= 0 {
		return nil, errs.InvalidArgument("expected_revision", "must be positive")
	}
	if req.Msg.Locale != "und" {
		return nil, errs.InvalidArgument("locale", "Map Theme locale must be und")
	}
	snapshot, err := internalSnapshot(req.Msg.Snapshot)
	if err != nil {
		return nil, err
	}
	revision := req.Msg.ExpectedRevision

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The collaboration admission check runs before this request reaches the
		// API, but the resource can be deleted or its revision can change while a
		// document is open. Lock the aggregate again at the final save boundary;
		// no snapshot may be accepted for a deleted theme.
		var current model.MapTheme
		if loadErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", themeID).
			Take(&current).Error; loadErr != nil {
			return loadErr
		}
		if current.EditVersion != revision {
			return errMapThemeRevisionConflict
		}
		if err := s.requireLockedMapThemeContributorsEdit(ctx, tx, themeID, req.Msg.ContributorMemberIds); err != nil {
			return err
		}
		result := tx.Model(&model.MapTheme{}).
			Where("id = ? AND edit_version = ?", themeID, req.Msg.ExpectedRevision).
			Updates(func() structured.Fields {
				updates := mapThemeSnapshotUpdates(snapshot)
				updates["edit_version"] = gorm.Expr("edit_version + 1")
				updates["updated_at"] = time.Now()
				return updates
			}())
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var current model.MapTheme
			if loadErr := tx.Select("id", "edit_version").Where("id = ?", themeID).Take(&current).Error; loadErr != nil {
				if stderrors.Is(loadErr, gorm.ErrRecordNotFound) {
					return gorm.ErrRecordNotFound
				}
				return loadErr
			}
			return errMapThemeRevisionConflict
		}
		if err := s.appendContentUpdatedAudit(ctx, tx, themeID, req.Msg.ContributorMemberIds[0]); err != nil {
			return err
		}
		revision++
		return nil
	})
	if err != nil {
		switch {
		case stderrors.Is(err, gorm.ErrRecordNotFound):
			return nil, errs.NotFound("map_theme", themeID)
		case stderrors.Is(err, errMapThemeRevisionConflict):
			return nil, errs.CollaborationConflict(
				intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
				"map theme changed; reload before saving",
			)
		case connect.CodeOf(err) != connect.CodeUnknown:
			return nil, err
		default:
			return nil, errs.Internal(err)
		}
	}
	return connect.NewResponse(&intrav1.SaveMapThemeSnapshotResponse{Success: true, Revision: revision, Locale: "und"}), nil
}

func (s *InternalMapService) LoadMapThemeSnapshot(
	ctx context.Context, req *connect.Request[intrav1.LoadMapThemeSnapshotRequest],
) (*connect.Response[intrav1.LoadMapThemeSnapshotResponse], error) {
	themeID, err := normalizeMapThemeID(req.Msg.ThemeId, "theme_id")
	if err != nil {
		return nil, err
	}
	if req.Msg.Locale != "und" {
		return nil, errs.InvalidArgument("locale", "Map Theme locale must be und")
	}
	theme, err := loadMapThemeSnapshotByID(ctx, s.db, themeID)
	if err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("map_theme", themeID)
		}
		return nil, errs.Internal(err)
	}
	snapshot, err := internalSnapshotProto(theme)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&intrav1.LoadMapThemeSnapshotResponse{Snapshot: snapshot, Revision: theme.EditVersion, Locale: "und"}), nil
}
