//go:build integration

package maptheme

import (
	"context"
	"sync"
	"testing"

	"connectrpc.com/connect"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func validMapThemeVariantInput(background string) *managev1.MapThemeVariantInput {
	return &managev1.MapThemeVariantInput{
		BackgroundColor: background, WaterColor: "#112233", LandColor: "#223344",
		RoadColor: "#334455", BuildingFillColor: "#445566", BuildingStrokeEnabled: true,
		BuildingStrokeColor: "#556677", CalloutLineColor: "#667788",
		CalloutTextColor: "#778899", CalloutBackgroundColor: "rgba(10,20,30,0.5)",
		CalloutDescriptionColor: "#8899aa", AttributionColor: "transparent",
		LabelTextColor: "#99aabb", ClusterColor: "#aabbcc",
		ClusterHoverColor: "#bbccdd", ClusterTextColor: "#ccddee",
		ClusterTextHoverColor: "#ddeeff", CalloutHoverLineColor: "#123",
		CalloutHoverTextColor: "#1234", CalloutHoverDescriptionColor: "#123456",
		CalloutHoverBackgroundColor: "#12345678",
	}
}

func validCreateMapThemeRequest(name string) *managev1.CreateMapThemeRequest {
	return &managev1.CreateMapThemeRequest{
		Name: name,
		Settings: &managev1.MapThemeSettings{
			CalloutScale: 1.25, CalloutOffsetX: 2, CalloutOffsetY: 3,
			CalloutFields: []string{"name", "address"}, AttributionFontSize: 12,
			ShowAreaLabels: true,
		},
		LightVariant: validMapThemeVariantInput("#ffffff"),
		DarkVariant:  validMapThemeVariantInput("#000000"),
	}
}

func documentVariantFromManage(input *managev1.MapThemeVariantInput) *intrav1.MapThemeDocumentVariant {
	return &intrav1.MapThemeDocumentVariant{
		BackgroundColor: input.BackgroundColor, WaterColor: input.WaterColor, LandColor: input.LandColor,
		RoadColor: input.RoadColor, BuildingFillColor: input.BuildingFillColor,
		BuildingStrokeEnabled: input.BuildingStrokeEnabled, BuildingStrokeColor: input.BuildingStrokeColor,
		CalloutLineColor: input.CalloutLineColor, CalloutTextColor: input.CalloutTextColor,
		CalloutBackgroundColor:  input.CalloutBackgroundColor,
		CalloutDescriptionColor: input.CalloutDescriptionColor, AttributionColor: input.AttributionColor,
		LabelTextColor: input.LabelTextColor, ClusterColor: input.ClusterColor,
		ClusterHoverColor: input.ClusterHoverColor, ClusterTextColor: input.ClusterTextColor,
		ClusterTextHoverColor:        input.ClusterTextHoverColor,
		CalloutHoverLineColor:        input.CalloutHoverLineColor,
		CalloutHoverTextColor:        input.CalloutHoverTextColor,
		CalloutHoverDescriptionColor: input.CalloutHoverDescriptionColor,
		CalloutHoverBackgroundColor:  input.CalloutHoverBackgroundColor,
	}
}

func validDocumentSnapshot(name string) *intrav1.MapThemeDocumentSnapshot {
	request := validCreateMapThemeRequest(name)
	return &intrav1.MapThemeDocumentSnapshot{
		Name: name,
		Settings: &intrav1.MapThemeDocumentSettings{
			CalloutScale: 1.5, CalloutOffsetX: 4, CalloutOffsetY: 6,
			CalloutFields: []string{"name", "city"}, AttributionFontSize: 14,
			ShowPoiLabels: true,
		},
		LightVariant: documentVariantFromManage(request.LightVariant),
		DarkVariant:  documentVariantFromManage(request.DarkVariant),
	}
}

func TestInternalMapServiceLoadsTypedSnapshotAndCASAllowsExactlyOneWriter(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	spiceDB := durableAudienceSpiceDB(t)
	admin := mapThemeAdminUser(t)
	ctx := mapThemeMemberContext(admin)
	themeService := mapThemeServiceForTest(t, db, spiceDB)
	created, err := themeService.CreateMapTheme(ctx, connect.NewRequest(validCreateMapThemeRequest("CAS "+integrationTestUUID())))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = themeService.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: created.Msg.Id}))
	})

	internalService := NewInternalMapService(db, spiceDB)
	loaded, err := internalService.LoadMapThemeSnapshot(context.Background(), connect.NewRequest(&intrav1.LoadMapThemeSnapshotRequest{ThemeId: created.Msg.Id, Locale: "und"}))
	require.NoError(t, err)
	require.EqualValues(t, 1, loaded.Msg.Revision)
	require.Equal(t, created.Msg.Name, loaded.Msg.Snapshot.Name)
	require.NotNil(t, loaded.Msg.Snapshot.LightVariant)
	require.NotNil(t, loaded.Msg.Snapshot.DarkVariant)

	requests := []*intrav1.SaveMapThemeSnapshotRequest{
		{ThemeId: created.Msg.Id, Locale: "und", ExpectedRevision: 1, Snapshot: validDocumentSnapshot("Writer A"), ContributorMemberIds: []string{admin.MemberID}},
		{ThemeId: created.Msg.Id, Locale: "und", ExpectedRevision: 1, Snapshot: validDocumentSnapshot("Writer B"), ContributorMemberIds: []string{admin.MemberID}},
	}
	errors := make([]error, len(requests))
	var wait sync.WaitGroup
	for i := range requests {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, errors[index] = internalService.SaveMapThemeSnapshot(context.Background(), connect.NewRequest(requests[index]))
		}(i)
	}
	wait.Wait()

	successes, conflicts := 0, 0
	for _, saveErr := range errors {
		if saveErr == nil {
			successes++
		} else if connect.CodeOf(saveErr) == connect.CodeFailedPrecondition {
			conflicts++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)

	loaded, err = internalService.LoadMapThemeSnapshot(context.Background(), connect.NewRequest(&intrav1.LoadMapThemeSnapshotRequest{ThemeId: created.Msg.Id, Locale: "und"}))
	require.NoError(t, err)
	require.EqualValues(t, 2, loaded.Msg.Revision)
	require.Contains(t, []string{"Writer A", "Writer B"}, loaded.Msg.Snapshot.Name)
}

func TestInternalMapServiceRejectsIncompleteSnapshotWithoutDBChanges(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceDB := durableAudienceSpiceDB(t)
	ctx := mapThemeAdminContext(t)
	created, err := mapThemeServiceForTest(t, db, spiceDB).CreateMapTheme(ctx, connect.NewRequest(validCreateMapThemeRequest("Invalid "+integrationTestUUID())))
	require.NoError(t, err)

	snapshot := validDocumentSnapshot("Must not persist")
	snapshot.DarkVariant = nil
	_, err = NewInternalMapService(db, spiceDB).SaveMapThemeSnapshot(context.Background(), connect.NewRequest(&intrav1.SaveMapThemeSnapshotRequest{
		ThemeId: created.Msg.Id, Locale: "und", ExpectedRevision: 1, Snapshot: snapshot,
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	loaded, loadErr := NewInternalMapService(db, spiceDB).LoadMapThemeSnapshot(context.Background(), connect.NewRequest(&intrav1.LoadMapThemeSnapshotRequest{ThemeId: created.Msg.Id, Locale: "und"}))
	require.NoError(t, loadErr)
	require.EqualValues(t, 1, loaded.Msg.Revision)
	require.Equal(t, created.Msg.Name, loaded.Msg.Snapshot.Name)
}
