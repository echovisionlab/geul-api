package ogpostgres

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestNewUnitsNamesEveryMissingPort(t *testing.T) {
	_, err := NewUnits(nil, Ports{})
	require.EqualError(t, err, "OG units: missing AllRequests, Projections, Reconciler, References, RenderConfig, Requests, db")
}

type stubSource struct {
	selection *managev1.OgTargetSelection
	err       error
}

func (s *stubSource) Handles(entityType string) bool { return entityType == "work" }

func (s *stubSource) Resolve(_ context.Context, _ *gorm.DB, _, _ string, selection *managev1.OgTargetSelection) ([]og.Request, error) {
	s.selection = selection
	return nil, s.err
}

func TestResolverAnswersAMissingRowAsAMissingEntity(t *testing.T) {
	source := &stubSource{err: gorm.ErrRecordNotFound}
	_, err := NewResolver(source).resolve(t.Context(), nil, "work", "id", og.Selection{Kind: og.SelectionLocale, Locale: "ko"})
	require.ErrorIs(t, err, og.ErrEntityNotFound)
	require.Equal(t, "ko", source.selection.GetLocale())

	_, err = NewResolver(source).resolve(t.Context(), nil, "post", "id", og.Selection{Kind: og.SelectionPrimary})
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
}

func TestJoinedEntryPointsRejectUnknownEntityTypes(t *testing.T) {
	err := CancelEntity(t.Context(), nil, managev1.OgEntityType_OG_ENTITY_TYPE_UNSPECIFIED, "id")
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
	require.ErrorContains(t, err, "OG_ENTITY_TYPE_UNSPECIFIED")

	planner := &Planner{}
	_, err = planner.RequestBulkReloadedWithDB(t.Context(), nil, "manual", "test", nil, nil)
	require.ErrorContains(t, err, "reloader is required")
	_, err = planner.RequestBulkReloadedWithDB(t.Context(), nil, "manual", "test", nil,
		func(context.Context, *gorm.DB) ([]og.Request, error) { return nil, nil })
	require.ErrorContains(t, err, "transaction is required")
}

func TestLifecycleEventPayloadCarriesTheTransition(t *testing.T) {
	locale, replacement, code := "ko", "replacement", " processing_failed "
	occurred := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	payload, err := lifecycleEventPayload(og.LifecycleEvent{
		Generation: og.Generation{ID: "generation", RunID: "run", LastErrorCode: &code},
		Target:     og.GenerationTarget{EntityType: "work", EntityID: "work-1", TargetKind: og.KindLocale, Locale: &locale},
		Status:     og.StatusSuperseded, ReplacementID: &replacement, OccurredAt: occurred,
	}, &og.AssetRef{AssetID: "asset", URL: "https://cdn.example.com/asset", FileSize: 1})
	require.NoError(t, err)
	var event managev1.OgGenerationLifecycleEvent
	require.NoError(t, proto.Unmarshal(payload, &event))
	require.Equal(t, "generation", event.GetGenerationId())
	require.Equal(t, managev1.OgGenerationStatus_OG_GENERATION_STATUS_SUPERSEDED, event.GetStatus())
	require.Equal(t, "ko", event.GetTarget().GetLocale().GetLocale())
	require.Equal(t, "processing_failed", event.GetErrorCode())
	require.Equal(t, "replacement", event.GetReplacementGenerationId())
	require.Equal(t, "asset", event.GetAsset().GetAssetId())
	require.True(t, event.GetOccurredAt().AsTime().Equal(occurred))

	_, err = lifecycleEventPayload(og.LifecycleEvent{Target: og.GenerationTarget{EntityType: "unknown"}}, nil)
	require.Error(t, err)
}
