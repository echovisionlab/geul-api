package maptheme

import (
	"context"
	stderrors "errors"
	"fmt"
	"maps"

	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

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

func internalSnapshotProto(theme *model.MapTheme) *intrav1.MapThemeDocumentSnapshot {
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
	}
}

func mapThemeVariantUpdates(prefix string, input Variant) structured.Fields {
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

func snapshotUpdates(snapshot *Snapshot) structured.Fields {
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
func (s *Service) requireLockedMapThemeContributorsEdit(
	ctx context.Context,
	tx *gorm.DB,
	themeID string,
	memberIDs []string,
) error {
	denied := domainerr.Denied("no permission to edit this map theme")
	if len(memberIDs) != 1 {
		return denied
	}
	memberID := memberIDs[0]
	if _, err := uuidutil.ParseCanonical(memberID, "contributor_member_ids"); err != nil {
		return domainerr.InvalidField("contributor_member_ids", "must contain exactly one canonical Member UUID")
	}
	can, err := policyv1.MapTheme.Edit(themeID)
	if err != nil {
		return domainerr.ResourceNotFound("theme", themeID).WithCause(err)
	}
	target, err := authorizationtarget.RequireLocked(ctx, tx, memberID)
	if err != nil {
		if authorizationtarget.FailedToDecide(err) {
			return err
		}
		return denied
	}
	actor, err := policyv1.NewAccountIdentityActor(target.IdentityID)
	if err != nil {
		return denied.WithCause(err)
	}
	allowed, err := s.spiceDB.CheckActorCan(ctx, actor, can)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return denied
	}
	return nil
}

// SaveCollaborationSnapshotCommand stores the snapshot the collaboration
// service materialized from a document read at ExpectedRevision.
type SaveCollaborationSnapshotCommand struct {
	ThemeID              string
	Locale               string
	ExpectedRevision     int64
	Snapshot             *SnapshotInput
	ContributorMemberIDs []string
}

// LoadCollaborationSnapshotQuery names the theme a collaboration document
// opens.
type LoadCollaborationSnapshotQuery struct {
	ThemeID string
	Locale  string
}

// CollaborationSnapshot is the stored snapshot of a theme and its revision.
type CollaborationSnapshot struct {
	Snapshot *intrav1.MapThemeDocumentSnapshot
	Revision int64
}

// SaveCollaborationSnapshot stores a collaboration snapshot and returns the
// new revision. The theme is locked again at the final save boundary: it can
// be deleted or its revision can change while a document is open, and no
// snapshot may be accepted for a deleted theme.
func (s *Service) SaveCollaborationSnapshot(ctx context.Context, command SaveCollaborationSnapshotCommand) (int64, error) {
	themeID, err := normalizeMapThemeID(command.ThemeID, "theme_id")
	if err != nil {
		return 0, err
	}
	if command.ExpectedRevision <= 0 {
		return 0, domainerr.InvalidField("expected_revision", "must be positive")
	}
	if command.Locale != "und" {
		return 0, domainerr.InvalidField("locale", "Map Theme locale must be und")
	}
	snapshot, err := command.Snapshot.snapshot("snapshot.")
	if err != nil {
		return 0, err
	}
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			var current model.MapTheme
			if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
				Where("id = ?", themeID).Take(&current).Error; err != nil {
				return err
			}
			if current.EditVersion != command.ExpectedRevision {
				return mapThemeChanged(fmt.Sprint(current.EditVersion))
			}
			return nil
		},
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			return s.requireLockedMapThemeContributorsEdit(ctx, tx, themeID, command.ContributorMemberIDs)
		}),
		Change: func(_ context.Context, tx *gorm.DB) (string, error) {
			return command.ContributorMemberIDs[0], writeSnapshot(tx, themeID, command.ExpectedRevision, snapshot, command.ExpectedRevision)
		},
		Audit: func(contributor string) mutation.Audit {
			return mutation.MemberAudit(contributor, sharedtelemetry.AuditMapThemeUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMapThemeContentUpdatedAuditRecord(metadata, themeID)
			})
		},
	})
	if stderrors.Is(err, gorm.ErrRecordNotFound) {
		return 0, domainerr.ResourceNotFound("map_theme", themeID)
	}
	if err != nil {
		return 0, err
	}
	return command.ExpectedRevision + 1, nil
}

func (s *Service) LoadCollaborationSnapshot(ctx context.Context, query LoadCollaborationSnapshotQuery) (CollaborationSnapshot, error) {
	themeID, err := normalizeMapThemeID(query.ThemeID, "theme_id")
	if err != nil {
		return CollaborationSnapshot{}, err
	}
	if query.Locale != "und" {
		return CollaborationSnapshot{}, domainerr.InvalidField("locale", "Map Theme locale must be und")
	}
	theme, err := loadMapThemeSnapshotByID(ctx, s.db, themeID)
	if err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return CollaborationSnapshot{}, domainerr.ResourceNotFound("map_theme", themeID)
		}
		return CollaborationSnapshot{}, err
	}
	return CollaborationSnapshot{Snapshot: internalSnapshotProto(theme), Revision: theme.EditVersion}, nil
}
