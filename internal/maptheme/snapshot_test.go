package maptheme_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	"github.com/echovisionlab/geul-api/internal/maptheme/mapthemetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// requireRevisionConflict asserts err is the collaboration conflict that
// reports current as the revision to reload.
func requireRevisionConflict(t *testing.T, err error, current string) {
	t.Helper()
	connectErr := errs.Wrap(err)
	require.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
	require.Len(t, connectErr.Details(), 1)
	detail, err := connectErr.Details()[0].Value()
	require.NoError(t, err)
	conflict, ok := detail.(*intrav1.CollaborationConflictDetail)
	require.True(t, ok)
	require.Equal(t, intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED, conflict.Reason)
	require.Equal(t, current, conflict.GetCurrentDocumentRevision())
}

func TestUpdateMapThemeSnapshotIsANoOpForTheSameNormalizedValues(t *testing.T) {
	f := newFixture(t, nil, nil)
	snapshot := mapthemetest.Snapshot("Snapshot source")
	snapshot.Settings.CalloutScale = 1.1 // Not exactly representable in the stored float32.
	theme := f.store.Seed(snapshot)
	input := snapshotInput("Snapshot source")
	input.Name = " Snapshot source "
	input.Settings.CalloutScale = 1.1
	input.LightVariant.BackgroundColor = " #ffffff "

	update, err := f.service.UpdateMapThemeSnapshot(newPrincipal(t).ctx, maptheme.UpdateMapThemeSnapshotCommand{
		ThemeID: theme.ID, ExpectedRevision: 1, Snapshot: input,
	})

	require.NoError(t, err)
	require.False(t, update.Changed)
	require.Equal(t, theme, update.Theme)
	stored, _ := f.store.Stored(theme.ID)
	require.Equal(t, theme, stored)
	require.Empty(t, f.audit.Records())
}

func TestUpdateMapThemeSnapshotStoresAChangeAtTheExpectedRevision(t *testing.T) {
	f := newFixture(t, nil, nil)
	theme := f.seed("Snapshot source")
	admin := newPrincipal(t)
	input := snapshotInput(" Changed snapshot ")
	input.Settings.ShowAreaLabels = false
	input.LightVariant.BuildingStrokeEnabled = false

	update, err := f.service.UpdateMapThemeSnapshot(admin.ctx, maptheme.UpdateMapThemeSnapshotCommand{
		ThemeID: theme.ID, ExpectedRevision: 1, Snapshot: input,
	})

	require.NoError(t, err)
	require.True(t, update.Changed)
	require.EqualValues(t, 2, update.Theme.Revision)
	require.Equal(t, "Changed snapshot", update.Theme.Snapshot.Name)
	require.False(t, update.Theme.Snapshot.Settings.ShowAreaLabels)
	require.False(t, update.Theme.Snapshot.LightVariant.BuildingStrokeEnabled)
	stored, _ := f.store.Stored(theme.ID)
	require.Equal(t, stored, update.Theme)
	require.Equal(t, []authztest.Check{checkOf(admin.identityID)(policyv1.MapTheme.Edit(theme.ID))}, f.checker.Checks())
	requireAudited(t, f, admin, [2]string{string(sharedtelemetry.AuditMapThemeUpdated), theme.ID})
	require.Equal(t, []string{"content"}, f.audit.Records()[0].ChangedFields)

	_, err = f.service.UpdateMapThemeSnapshot(admin.ctx, maptheme.UpdateMapThemeSnapshotCommand{
		ThemeID: theme.ID, ExpectedRevision: 1, Snapshot: snapshotInput("Stale"),
	})
	requireRevisionConflict(t, err, "2")
	require.Len(t, f.audit.Records(), 1)
}

func TestUpdateMapThemeSnapshotChecksAuthorityBeforeTheRevision(t *testing.T) {
	f := newFixture(t, nil, nil)
	theme := f.seed("Snapshot protected")
	f.checker.Deny(policyv1.MapTheme.Edit(theme.ID))

	for _, revision := range []int64{1, 999} {
		before := len(f.units.Steps())
		_, err := f.service.UpdateMapThemeSnapshot(newPrincipal(t).ctx, maptheme.UpdateMapThemeSnapshotCommand{
			ThemeID: theme.ID, ExpectedRevision: revision, Snapshot: snapshotInput("Unauthorized change"),
		})
		require.Equal(t, connect.CodePermissionDenied, codeOf(err), "an Author does not learn the conflict state")
		require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepRollback}, f.stepsSince(before))
	}
	stored, _ := f.store.Stored(theme.ID)
	require.Equal(t, theme, stored)
}

func TestUpdateMapThemeSnapshotRollsBackWhenTheAuditFails(t *testing.T) {
	auditDown := errors.New("audit unavailable")
	f := newFixture(t, nil, &audittest.Recorder{Err: auditDown})
	theme := f.seed("Snapshot protected")

	update, err := f.service.UpdateMapThemeSnapshot(newPrincipal(t).ctx, maptheme.UpdateMapThemeSnapshotCommand{
		ThemeID: theme.ID, ExpectedRevision: 1, Snapshot: snapshotInput("Must roll back"),
	})

	require.ErrorIs(t, err, auditDown)
	require.False(t, update.Changed)
	stored, _ := f.store.Stored(theme.ID)
	require.Equal(t, theme, stored)
	require.Equal(t, mutation.StepRollback, f.units.Steps()[len(f.units.Steps())-1])
}

func TestUpdateMapThemeSnapshotValidatesBeforeTheTransaction(t *testing.T) {
	f := newFixture(t, nil, nil)
	theme := f.seed("Snapshot source")
	missingSettings := snapshotInput("Missing settings")
	missingSettings.Settings = nil
	for name, command := range map[string]maptheme.UpdateMapThemeSnapshotCommand{
		"revision":         {ThemeID: theme.ID, ExpectedRevision: 0, Snapshot: snapshotInput("x")},
		"missing snapshot": {ThemeID: theme.ID, ExpectedRevision: 1},
		"missing settings": {ThemeID: theme.ID, ExpectedRevision: 1, Snapshot: missingSettings},
		"theme":            {ThemeID: "theme", ExpectedRevision: 1, Snapshot: snapshotInput("x")},
	} {
		_, err := f.service.UpdateMapThemeSnapshot(newPrincipal(t).ctx, command)
		require.Equal(t, connect.CodeInvalidArgument, codeOf(err), name)
	}
	require.Empty(t, f.units.Steps())
}

func TestUpdateMapThemeSnapshotAnswersAMissingThemeBeforeAuthorization(t *testing.T) {
	f := newFixture(t, nil, nil)
	_, err := f.service.UpdateMapThemeSnapshot(newPrincipal(t).ctx, maptheme.UpdateMapThemeSnapshotCommand{
		ThemeID: uuid.NewString(), ExpectedRevision: 1, Snapshot: snapshotInput("x"),
	})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
	require.Empty(t, f.checker.Checks())
}

// collaborationSave is a valid collaboration save of themeID at revision
// by memberID.
func collaborationSave(themeID string, revision int64, memberID, name string) *intrav1.SaveMapThemeSnapshotRequest {
	request := createRequest(name)
	variant := func(input *maptheme.Variant) *intrav1.MapThemeDocumentVariant {
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
	snapshot := mapthemetest.Snapshot(name)
	return &intrav1.SaveMapThemeSnapshotRequest{
		ThemeId: themeID, Locale: "und", ExpectedRevision: revision, ContributorMemberIds: []string{memberID},
		Snapshot: &intrav1.MapThemeDocumentSnapshot{
			Name: request.Name,
			Settings: &intrav1.MapThemeDocumentSettings{
				CalloutScale: 1.5, CalloutOffsetX: 4, CalloutOffsetY: 6,
				CalloutFields: []string{"name", "city"}, AttributionFontSize: 14, ShowPoiLabels: true,
			},
			LightVariant: variant(&snapshot.LightVariant),
			DarkVariant:  variant(&snapshot.DarkVariant),
		},
	}
}

// contributor is a Member the store resolves to a live Identity.
func (f *fixture) contributor() (memberID, identityID string) {
	memberID, identityID = uuid.NewString(), uuid.NewString()
	f.store.SeedContributor(memberID, identityID)
	return memberID, identityID
}

func TestSaveCollaborationSnapshotStoresTheSnapshotForAnAllowedContributor(t *testing.T) {
	f := newFixture(t, &authztest.Checker{}, nil)
	theme := f.seed("Collaboration source")
	memberID, identityID := f.contributor()
	f.checker.For(identityID).Allow(policyv1.MapTheme.Edit(theme.ID))
	ctx := newPrincipal(t).ctx

	response, err := f.internal.SaveMapThemeSnapshot(ctx, connect.NewRequest(collaborationSave(theme.ID, 1, memberID, "Audited content")))

	require.NoError(t, err)
	require.True(t, response.Msg.Success)
	require.EqualValues(t, 2, response.Msg.Revision)
	require.Equal(t, "und", response.Msg.Locale)
	stored, _ := f.store.Stored(theme.ID)
	require.EqualValues(t, 2, stored.Revision)
	require.Equal(t, "Audited content", stored.Snapshot.Name)
	require.Equal(t, []string{"name", "city"}, stored.Snapshot.Settings.CalloutFields)
	require.Equal(t, []authztest.Check{checkOf(identityID)(policyv1.MapTheme.Edit(theme.ID))}, f.checker.Checks())
	records := f.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditMapThemeUpdated, records[0].Action)
	require.Equal(t, "map_theme", records[0].TargetType)
	require.Equal(t, theme.ID, records[0].TargetID)
	require.Equal(t, memberID, records[0].MemberID, "the save is attributed to its contributor")
	require.Equal(t, []string{"content"}, records[0].ChangedFields)
	require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepCommit}, f.units.Steps())

	loaded, err := f.internal.LoadMapThemeSnapshot(ctx, connect.NewRequest(&intrav1.LoadMapThemeSnapshotRequest{ThemeId: theme.ID, Locale: "und"}))
	require.NoError(t, err)
	require.EqualValues(t, 2, loaded.Msg.Revision)
	require.Equal(t, "und", loaded.Msg.Locale)
	require.Equal(t, "Audited content", loaded.Msg.Snapshot.Name)
	require.Equal(t, float64(1.5), loaded.Msg.Snapshot.Settings.CalloutScale)
	require.Equal(t, stored.Snapshot.DarkVariant.BackgroundColor, loaded.Msg.Snapshot.DarkVariant.BackgroundColor)
}

func TestSaveCollaborationSnapshotAcceptsExactlyOneSaveOfARevision(t *testing.T) {
	f := newFixture(t, nil, nil)
	theme := f.seed("CAS")
	memberID, _ := f.contributor()

	_, first := f.internal.SaveMapThemeSnapshot(t.Context(), connect.NewRequest(collaborationSave(theme.ID, 1, memberID, "Writer A")))
	_, second := f.internal.SaveMapThemeSnapshot(t.Context(), connect.NewRequest(collaborationSave(theme.ID, 1, memberID, "Writer B")))

	require.NoError(t, first)
	requireRevisionConflict(t, second, "2")
	stored, _ := f.store.Stored(theme.ID)
	require.Equal(t, "Writer A", stored.Snapshot.Name)
	require.EqualValues(t, 2, stored.Revision)
}

func TestSaveCollaborationSnapshotReportsAStaleRevisionBeforeAuthorization(t *testing.T) {
	f := newFixture(t, &authztest.Checker{}, nil)
	theme := f.seed("Stale")

	_, err := f.internal.SaveMapThemeSnapshot(t.Context(), connect.NewRequest(collaborationSave(theme.ID, 3, uuid.NewString(), "Stale")))

	requireRevisionConflict(t, err, "1")
	require.Empty(t, f.checker.Checks())
}

func TestSaveCollaborationSnapshotDeniesAContributorWithoutEditAuthority(t *testing.T) {
	spiceDown := errors.New("SpiceDB unavailable")
	lookupDown := connect.NewError(connect.CodeInternal, errors.New("member lookup failed"))
	for name, test := range map[string]struct {
		prepare func(f *fixture, themeID string) []string
		code    connect.Code
		message string
	}{
		"without the edit permission": {
			prepare: func(f *fixture, themeID string) []string {
				memberID, identityID := f.contributor()
				f.checker.For(identityID).Deny(policyv1.MapTheme.Edit(themeID))
				return []string{memberID}
			},
			code: connect.CodePermissionDenied, message: "no permission to edit this map theme",
		},
		"an unknown Member": {
			prepare: func(*fixture, string) []string { return []string{uuid.NewString()} },
			code:    connect.CodePermissionDenied, message: "no permission to edit this map theme",
		},
		"two contributors": {
			prepare: func(f *fixture, _ string) []string {
				first, _ := f.contributor()
				second, _ := f.contributor()
				return []string{first, second}
			},
			code: connect.CodePermissionDenied, message: "no permission to edit this map theme",
		},
		"no contributor": {
			prepare: func(*fixture, string) []string { return nil },
			code:    connect.CodePermissionDenied, message: "no permission to edit this map theme",
		},
		"a malformed Member ID": {
			prepare: func(*fixture, string) []string { return []string{"member"} },
			code:    connect.CodeInvalidArgument, message: "contributor_member_ids",
		},
		"an unavailable SpiceDB": {
			prepare: func(f *fixture, _ string) []string {
				memberID, _ := f.contributor()
				f.checker.Fail(spiceDown)
				return []string{memberID}
			},
			code: connect.CodeUnavailable, message: "SpiceDB",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, authztest.AllowAll(), nil)
			theme := f.seed("Protected")
			request := collaborationSave(theme.ID, 1, "", "Must not persist")
			request.ContributorMemberIds = test.prepare(f, theme.ID)

			_, err := f.internal.SaveMapThemeSnapshot(t.Context(), connect.NewRequest(request))

			require.Equal(t, test.code, connect.CodeOf(err))
			require.ErrorContains(t, err, test.message)
			stored, _ := f.store.Stored(theme.ID)
			require.Equal(t, theme, stored)
			require.Empty(t, f.audit.Records())
		})
	}

	t.Run("a failed contributor lookup", func(t *testing.T) {
		f := newFixture(t, nil, nil)
		theme := f.seed("Protected")
		memberID, _ := f.contributor()
		f.store.FailContributors(lookupDown)

		_, err := f.internal.SaveMapThemeSnapshot(t.Context(), connect.NewRequest(collaborationSave(theme.ID, 1, memberID, "x")))

		require.ErrorIs(t, err, lookupDown, "a failure to decide is not a denial")
	})
}

func TestSaveCollaborationSnapshotRollsBackWhenTheAuditFails(t *testing.T) {
	auditDown := errors.New("audit unavailable")
	f := newFixture(t, nil, &audittest.Recorder{Err: auditDown})
	theme := f.seed("Rollback source")
	memberID, _ := f.contributor()

	_, err := f.internal.SaveMapThemeSnapshot(t.Context(), connect.NewRequest(collaborationSave(theme.ID, 1, memberID, "Must roll back")))

	require.ErrorIs(t, err, auditDown)
	stored, _ := f.store.Stored(theme.ID)
	require.Equal(t, theme, stored)
}

func TestSaveCollaborationSnapshotValidatesBeforeTheTransaction(t *testing.T) {
	f := newFixture(t, nil, nil)
	theme := f.seed("Invalid")
	memberID, _ := f.contributor()
	for name, test := range map[string]struct {
		edit    func(*intrav1.SaveMapThemeSnapshotRequest)
		message string
	}{
		"a missing dark variant": {func(r *intrav1.SaveMapThemeSnapshotRequest) { r.Snapshot.DarkVariant = nil }, "snapshot.dark_variant is required"},
		"a missing snapshot":     {func(r *intrav1.SaveMapThemeSnapshotRequest) { r.Snapshot = nil }, "snapshot is required"},
		"a localized locale":     {func(r *intrav1.SaveMapThemeSnapshotRequest) { r.Locale = "en" }, "Map Theme locale must be und"},
		"no revision":            {func(r *intrav1.SaveMapThemeSnapshotRequest) { r.ExpectedRevision = 0 }, "expected_revision"},
		"an invalid color":       {func(r *intrav1.SaveMapThemeSnapshotRequest) { r.Snapshot.LightVariant.LandColor = "red" }, "light_variant.land_color"},
	} {
		request := collaborationSave(theme.ID, 1, memberID, "Must not persist")
		test.edit(request)
		_, err := f.internal.SaveMapThemeSnapshot(t.Context(), connect.NewRequest(request))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), name)
		require.ErrorContains(t, err, test.message, name)
	}
	require.Empty(t, f.units.Steps())
}

func TestCollaborationSnapshotsOfAMissingThemeAreNotFound(t *testing.T) {
	f := newFixture(t, nil, nil)
	memberID, _ := f.contributor()
	missing := uuid.NewString()

	_, err := f.internal.SaveMapThemeSnapshot(t.Context(), connect.NewRequest(collaborationSave(missing, 1, memberID, "Gone")))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.ErrorContains(t, err, "map_theme not found")
	_, err = f.internal.LoadMapThemeSnapshot(t.Context(), connect.NewRequest(&intrav1.LoadMapThemeSnapshotRequest{ThemeId: missing, Locale: "und"}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.ErrorContains(t, err, "map_theme not found")
	_, err = f.internal.LoadMapThemeSnapshot(t.Context(), connect.NewRequest(&intrav1.LoadMapThemeSnapshotRequest{ThemeId: missing, Locale: "ko"}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
