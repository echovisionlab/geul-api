package label_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/label"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func TestSetLabelImageBindsTheLogoAndRegeneratesTheOGImage(t *testing.T) {
	h := newHarness(t)
	h.store.OGRunID = "run-1"
	caller := newPrincipal()
	value := h.seedLabel(t, "Logo")
	light := &commonv1.AssetRef{AssetId: "light-asset"}
	h.store.PutReadyLogo("file-light", light)

	update, err := h.service.SetLabelImage(caller.context(t), label.SetLabelImageCommand{LabelID: value.ID, FileID: "file-light", Variant: label.LogoVariantLight})

	require.NoError(t, err)
	require.Same(t, light, update.Light)
	require.Nil(t, update.Dark)
	require.Equal(t, "run-1", *update.OgGenerationRunID)
	stored, _ := h.store.Label(value.ID)
	require.Equal(t, "file-light", *stored.LogoLightFileID)
	require.Equal(t, map[label.LogoVariant]labeltestBinding{label.LogoVariantLight: {FileID: "file-light"}}, bindings(h, value.ID))
	require.Equal(t, "label_logo_updated", h.store.OGRequests()[0].Reason)
	require.Equal(t, []label.ContentUpdate{{LabelID: value.ID, Kind: label.MediaUpdate}}, h.store.Updates())
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditCollectionOperationAdded, records[0].CollectionOperation)
	require.Equal(t, "file-light", records[0].AssetID)

	t.Run("the same file again changes nothing", func(t *testing.T) {
		_, err := h.service.SetLabelImage(caller.context(t), label.SetLabelImageCommand{LabelID: value.ID, FileID: "file-light", Variant: label.LogoVariantLight})
		require.NoError(t, err)
		require.Len(t, h.audit.Records(), 1)
		require.Len(t, h.store.OGRequests(), 1)
	})
	t.Run("a second logo leaves the effective logo and OG image alone", func(t *testing.T) {
		_, err := h.service.SetLabelImage(caller.context(t), label.SetLabelImageCommand{LabelID: value.ID, FileID: "file-dark", Variant: label.LogoVariantDark})
		require.NoError(t, err)
		require.Len(t, h.store.OGRequests(), 1)
	})
	t.Run("removing the light logo falls back to the dark one", func(t *testing.T) {
		runID, err := h.service.DeleteLabelImage(caller.context(t), label.DeleteLabelImageCommand{LabelID: value.ID, Variant: label.LogoVariantLight})
		require.NoError(t, err)
		require.Equal(t, "run-1", *runID)
		require.Equal(t, "label_logo_removed", h.store.OGRequests()[1].Reason)
		require.NotContains(t, bindings(h, value.ID), label.LogoVariantLight)
	})
	t.Run("removing the last logo cancels the OG image", func(t *testing.T) {
		runID, err := h.service.DeleteLabelImage(caller.context(t), label.DeleteLabelImageCommand{LabelID: value.ID, Variant: label.LogoVariantDark})
		require.NoError(t, err)
		require.Nil(t, runID)
		require.Equal(t, []string{value.ID}, h.store.OGCancels())
		stored, _ := h.store.Label(value.ID)
		require.Nil(t, stored.OgAssetID)
	})
}

type labeltestBinding = struct{ FileID string }

func bindings(h *harness, labelID string) map[label.LogoVariant]labeltestBinding {
	result := map[label.LogoVariant]labeltestBinding{}
	for variant, binding := range h.store.Bindings(labelID) {
		result[variant] = labeltestBinding{FileID: binding.FileID}
	}
	return result
}

func TestLabelImageRejections(t *testing.T) {
	h := newHarness(t)
	caller := newPrincipal()
	value := h.seedLabel(t, "Logo")

	_, err := h.service.SetLabelImage(caller.context(t), label.SetLabelImageCommand{LabelID: value.ID, FileID: "file", Variant: label.LogoVariantUnsupported})
	requireCode(t, err, connect.CodeInvalidArgument, "variant: unsupported theme asset variant")
	_, err = h.service.DeleteLabelImage(caller.context(t), label.DeleteLabelImageCommand{LabelID: "00000000-0000-4000-8000-000000000004", Variant: label.LogoVariantLight})
	requireCode(t, err, connect.CodeNotFound, "label not found: 00000000-0000-4000-8000-000000000004")
	manage, err := policyv1.Label.Manage(value.ID)
	require.NoError(t, err)
	h.checker.Checker = authztest.AllowAll().Deny(manage, nil)
	_, err = h.service.SetLabelImage(caller.context(t), label.SetLabelImageCommand{LabelID: value.ID, FileID: "file", Variant: label.LogoVariantLight})
	requireCode(t, err, connect.CodeNotFound, "label not found: "+value.ID)
	require.Empty(t, h.store.Bindings(value.ID))
}

func TestGetLabelEditorDataNeedsViewAndListsTheCallersActions(t *testing.T) {
	h := newHarness(t)
	caller := newPrincipal()
	value := h.seedLabel(t, "Editor", func(value *label.Label) {
		value.LogoDarkFileID = stringPointer("dark-file")
		value.OgAssetID = stringPointer("og-asset")
	})
	og := &commonv1.AssetRef{AssetId: "og-asset"}
	h.store.PutReadyAsset("og-asset", og)
	edit, err := policyv1.Label.Edit(value.ID)
	require.NoError(t, err)
	remove, err := policyv1.Label.RemoveOwner(value.ID)
	require.NoError(t, err)
	h.checker.Checker = authztest.AllowAll().Deny(edit, nil).Deny(remove, nil)

	data, err := h.service.GetLabelEditorData(caller.context(t), label.GetLabelEditorDataQuery{ID: value.ID})

	require.NoError(t, err)
	require.Equal(t, "Editor", data.Label.Name)
	require.Same(t, og, data.Label.OgImage, "a Label with an effective logo shows its OG image")
	require.Equal(t, []label.Action{label.ActionManageShareLinks, label.ActionDelete, label.ActionManageParticipants, label.ActionPublish}, data.AllowedActions)

	view, err := policyv1.Label.View(value.ID)
	require.NoError(t, err)
	h.checker.Checker = authztest.AllowAll().Deny(view, nil)
	_, err = h.service.GetLabelEditorData(caller.context(t), label.GetLabelEditorDataQuery{ID: value.ID})
	requireCode(t, err, connect.CodeNotFound, "label not found: "+value.ID)
}

func TestListLabelsShowsOnlyPublishedLabelsAndAdministratorsSeeAll(t *testing.T) {
	h := newHarness(t)
	caller := newPrincipal()
	published := h.seedLabel(t, "Published", func(value *label.Label) { value.Status = label.StatusPublished })
	draft := h.seedLabel(t, "Draft")
	h.store.PutCounts(draft.ID, 2, 3)

	page, err := h.service.ListLabels(caller.context(t), label.ListLabelsQuery{})
	require.NoError(t, err)
	require.Len(t, page.Labels, 1)
	require.Equal(t, published.ID, page.Labels[0].Label.ID)
	require.Equal(t, label.Pagination{Total: 1, Limit: 20}, page.Pagination)

	admin, err := h.service.ListLabelsAdmin(caller.context(t), label.ListLabelsQuery{Limit: 1})
	require.NoError(t, err)
	require.Equal(t, label.Pagination{Total: 2, Limit: 1, HasMore: true}, admin.Pagination)

	all, err := h.service.ListLabelsAdmin(caller.context(t), label.ListLabelsQuery{})
	require.NoError(t, err)
	counts := map[string][2]int32{}
	for _, item := range all.Labels {
		counts[item.Label.Label.ID] = [2]int32{item.ArtistCount, item.ReleaseCount}
	}
	require.Equal(t, map[string][2]int32{published.ID: {0, 0}, draft.ID: {2, 3}}, counts)

	list, err := policyv1.Label.List()
	require.NoError(t, err)
	h.checker.Checker = authztest.AllowAll().Deny(list, nil)
	_, err = h.service.ListLabelsAdmin(caller.context(t), label.ListLabelsQuery{})
	requireCode(t, err, connect.CodePermissionDenied, "admin access required")
}

func TestCheckLabelSlugAvailable(t *testing.T) {
	h := newHarness(t)
	caller := newPrincipal()
	value := h.seedLabel(t, "Slugged", func(value *label.Label) { value.Slug = stringPointer("taken") })
	h.store.PutRoute("routed")

	for _, test := range []struct {
		slug      string
		exclude   *string
		available bool
	}{
		{slug: "taken"}, {slug: "taken", exclude: &value.ID, available: true}, {slug: "routed"}, {slug: "free", available: true},
	} {
		available, err := h.service.CheckLabelSlugAvailable(caller.context(t), label.CheckLabelSlugAvailableQuery{Slug: test.slug, ExcludeLabelID: test.exclude})
		require.NoError(t, err)
		require.Equal(t, test.available, available, test.slug)
	}
	_, err := h.service.CheckLabelSlugAvailable(t.Context(), label.CheckLabelSlugAvailableQuery{Slug: "free"})
	requireCode(t, err, connect.CodeUnauthenticated, "authentication required")
}
