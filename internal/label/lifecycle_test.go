package label_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func TestCreateLabelStoresTheLabelAndMakesTheCallerItsOwner(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())
	parent := h.seedLabel(t, "Parent")
	slug := "new-label"
	country := " KR "

	view, err := h.service.CreateLabel(caller.context(t), label.CreateLabelCommand{
		Name: " New Label ", Slug: &slug, CountryCode: &country, ParentLabelID: &parent.ID,
	})

	require.NoError(t, err)
	require.Equal(t, "New Label", view.Name)
	require.Equal(t, "KR", *view.Label.CountryCode)
	require.Equal(t, label.StatusDraft, view.Label.Status)
	require.Equal(t, "en", view.Label.SourceLocale)
	require.NotEmpty(t, view.Revision)
	require.Nil(t, view.OgImage, "a Label without a logo shows no OG image")
	require.Equal(t, map[string]string{caller.memberID: entityparticipant.RoleOwner}, h.store.Participants(view.Label.ID))
	title, ok := h.store.Locale(view.Label.ID, "en")
	require.True(t, ok)
	require.Equal(t, "New Label", *title.Title)
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditLabelCreated}, auditActions(h.audit))
	policy, err := policyv1.Label.TouchPolicy(view.Label.ID)
	require.NoError(t, err)
	owner, err := policyv1.NewAccountIdentityActor(caller.identityID)
	require.NoError(t, err)
	ownerTouch, err := policyv1.Label.TouchOwner(view.Label.ID, owner)
	require.NoError(t, err)
	parentTouch, err := policyv1.Label.TouchParent(view.Label.ID, parent.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{policy, ownerTouch, parentTouch}}, h.checker.Applied())
	require.Equal(t, []mutation.Step{
		mutation.StepAuthorizationLock, mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit,
		mutation.StepRelationships, mutation.StepCommit,
	}, h.units.Steps())
	require.Equal(t, 2, h.store.ParentGraphLocks(), "a create with a parent takes the hierarchy lock first and validates under it")
}

func TestCreateLabelRejectsBeforeStoringAnything(t *testing.T) {
	admin, err := policyv1.Platform.IsAdmin()
	require.NoError(t, err)
	for _, test := range []struct {
		name    string
		prepare func(*harness) label.CreateLabelCommand
		code    connect.Code
		message string
	}{
		{
			name:    "blank name",
			prepare: func(*harness) label.CreateLabelCommand { return label.CreateLabelCommand{Name: "  "} },
			code:    connect.CodeInvalidArgument, message: "name is required",
		},
		{
			name: "taken slug",
			prepare: func(h *harness) label.CreateLabelCommand {
				h.seedLabel(t, "Taken", func(value *label.Label) { value.Slug = stringPointer("taken") })
				return label.CreateLabelCommand{Name: "New", Slug: stringPointer("taken")}
			},
			code: connect.CodeAlreadyExists, message: "label with slug 'taken' already exists",
		},
		{
			name: "invalid slug",
			prepare: func(*harness) label.CreateLabelCommand {
				return label.CreateLabelCommand{Name: "New", Slug: stringPointer("a/b")}
			},
			code: connect.CodeInvalidArgument,
		},
		{
			name: "route taken",
			prepare: func(h *harness) label.CreateLabelCommand {
				h.store.PutRoute("routed")
				return label.CreateLabelCommand{Name: "New", Slug: stringPointer("routed")}
			},
			code: connect.CodeAlreadyExists,
		},
		{
			name: "not an administrator",
			prepare: func(h *harness) label.CreateLabelCommand {
				h.checker.Checker = (&authztest.Checker{}).Deny(admin, nil)
				return label.CreateLabelCommand{Name: "New"}
			},
			code: connect.CodePermissionDenied, message: "admin access required",
		},
		{
			name: "missing parent",
			prepare: func(*harness) label.CreateLabelCommand {
				return label.CreateLabelCommand{Name: "New", ParentLabelID: stringPointer("00000000-0000-4000-8000-000000000001")}
			},
			code: connect.CodeInvalidArgument, message: "parent_label_id: label does not exist",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			caller := h.member(newPrincipal())
			command := test.prepare(h)

			_, err := h.service.CreateLabel(caller.context(t), command)

			requireCode(t, err, test.code, test.message)
			require.Empty(t, h.audit.Records())
			require.Empty(t, h.checker.Applied())
		})
	}
}

func TestCreateLabelRollsBackAndCompensatesWhenTheAuditFails(t *testing.T) {
	auditDown := errors.New("audit unavailable")
	h := newHarness(t, withAudit(&audittest.Recorder{Err: auditDown}))
	caller := h.member(newPrincipal())

	_, err := h.service.CreateLabel(caller.context(t), label.CreateLabelCommand{Name: "Rolled back"})

	require.ErrorIs(t, err, auditDown)
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	require.Empty(t, h.checker.Applied(), "the relationship batch is written after the audit")
	page, _, listErr := h.store.ListLabels(t.Context(), label.ListPage{Limit: 10})
	require.NoError(t, listErr)
	require.Empty(t, page)
}

func TestPublishLabelLocksTheRowThenStampsPublicationOnce(t *testing.T) {
	h := newHarness(t)
	caller := newPrincipal()
	value := h.seedLabel(t, "Publish me")

	published, err := h.service.PublishLabel(caller.context(t), label.PublishLabelCommand{ID: value.ID})

	require.NoError(t, err)
	require.True(t, published.Changed)
	require.Equal(t, label.StatusPublished, published.Status)
	require.NotNil(t, published.PublishedAt)
	stored, _ := h.store.Label(value.ID)
	require.Equal(t, label.StatusPublished, stored.Status)
	require.Equal(t, published.PublishedAt.UTC(), stored.PublishedAt.UTC())
	require.Equal(t, []label.ContentUpdate{{LabelID: value.ID, Kind: label.StateUpdate}}, h.store.Updates())
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditLabelUpdated}, auditActions(h.audit))
	require.Equal(t, sharedtelemetry.AuditStatePublished, h.audit.Records()[0].NewState)
	require.Equal(t, []mutation.Step{
		mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepOutbox, mutation.StepCommit,
	}, h.units.Steps())
	publish, err := policyv1.Label.Publish(value.ID)
	require.NoError(t, err)
	require.Equal(t, []authztest.Check{authztest.Of(caller.identityID, publish, nil)}, h.checker.Checks())

	again, err := h.service.PublishLabel(caller.context(t), label.PublishLabelCommand{ID: value.ID})
	require.NoError(t, err)
	require.False(t, again.Changed)
	require.Len(t, h.audit.Records(), 1, "an unchanged lifecycle is not audited")
	require.Len(t, h.store.Updates(), 1)

	unpublished, err := h.service.UnpublishLabel(caller.context(t), label.UnpublishLabelCommand{ID: value.ID})
	require.NoError(t, err)
	require.True(t, unpublished.Changed)
	require.Equal(t, label.StatusDraft, unpublished.Status)
	require.NotNil(t, unpublished.PublishedAt, "unpublishing keeps the first publication time")
}

func TestLabelLifecycleMasksAMissingOrForbiddenLabelAsNotFound(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*harness, principal, label.Label) string
	}{
		{name: "missing", prepare: func(*harness, principal, label.Label) string { return "00000000-0000-4000-8000-000000000009" }},
		{name: "denied", prepare: func(h *harness, _ principal, value label.Label) string {
			publish, err := policyv1.Label.Publish(value.ID)
			require.NoError(t, err)
			h.checker.Checker = authztest.AllowAll().Deny(publish, nil)
			return value.ID
		}},
		{name: "inactive principal", prepare: func(h *harness, caller principal, value label.Label) string {
			h.fence.Deactivate(caller.identityID)
			return value.ID
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			caller := newPrincipal()
			value := h.seedLabel(t, "Hidden")
			id := test.prepare(h, caller, value)

			_, err := h.service.PublishLabel(caller.context(t), label.PublishLabelCommand{ID: id})

			requireCode(t, err, connect.CodeNotFound, "label not found: "+id)
			stored, _ := h.store.Label(value.ID)
			require.Equal(t, label.StatusDraft, stored.Status)
			require.Empty(t, h.audit.Records())
		})
	}
}

func TestDeleteLabelChecksThePreviewAndRemovesEverythingTheLabelOwns(t *testing.T) {
	h := newHarness(t)
	caller := newPrincipal()
	value := h.seedLabel(t, "Doomed")
	child := h.seedLabel(t, "Child", func(child *label.Label) { child.ParentLabelID = &value.ID })
	h.store.PutImpacts(value.ID, label.DeletionImpact{Kind: label.ImpactArtist, EntityID: "artist-1", DisplayName: "Artist"})
	policy, err := policyv1.Label.TouchPolicy(value.ID)
	require.NoError(t, err)
	deletePolicy, err := policyv1.Label.DeletePolicy(value.ID)
	require.NoError(t, err)
	h.checker.Snapshot = []policyv1.RelationshipMutation{policy}

	preview, err := h.service.PreviewDeleteLabel(caller.context(t), label.PreviewDeleteLabelQuery{ID: value.ID})
	require.NoError(t, err)
	require.Equal(t, []label.DeletionImpact{
		{Kind: label.ImpactChildLabel, EntityID: child.ID, DisplayName: "Child"},
		{Kind: label.ImpactArtist, EntityID: "artist-1", DisplayName: "Artist"},
	}, preview.Impacts)

	err = h.service.DeleteLabel(caller.context(t), label.DeleteLabelCommand{ID: value.ID, PreviewRevision: "stale"})
	requireCode(t, err, connect.CodeFailedPrecondition, "label usage changed; review deletion impact again")
	_, kept := h.store.Label(value.ID)
	require.True(t, kept)

	require.NoError(t, h.service.DeleteLabel(caller.context(t), label.DeleteLabelCommand{ID: value.ID, PreviewRevision: preview.Revision}))

	_, exists := h.store.Label(value.ID)
	require.False(t, exists)
	_, documentExists := h.store.Document(parseDocumentID(t, value.ContentDocumentID))
	require.False(t, documentExists)
	require.Equal(t, []string{value.ID}, h.store.DeletedShareLinks())
	require.Equal(t, []string{value.ID}, h.store.OGCancels())
	require.Equal(t, [][]policyv1.RelationshipMutation{{deletePolicy}}, h.checker.Applied())
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditLabelDeleted}, auditActions(h.audit))

	err = h.service.DeleteLabel(caller.context(t), label.DeleteLabelCommand{ID: value.ID})
	requireCode(t, err, connect.CodeFailedPrecondition, "delete preview is required")
}

func TestDeleteLabelHidesALabelTheCallerMayNotDelete(t *testing.T) {
	h := newHarness(t)
	caller := newPrincipal()
	value := h.seedLabel(t, "Kept")
	deleteCan, err := policyv1.Label.Delete(value.ID)
	require.NoError(t, err)
	h.checker.Checker = authztest.AllowAll().Deny(deleteCan, nil)

	_, err = h.service.PreviewDeleteLabel(caller.context(t), label.PreviewDeleteLabelQuery{ID: value.ID})
	requireCode(t, err, connect.CodeNotFound, "label not found: "+value.ID)
	err = h.service.DeleteLabel(caller.context(t), label.DeleteLabelCommand{ID: value.ID, PreviewRevision: "any"})
	requireCode(t, err, connect.CodeNotFound, "label not found: "+value.ID)
	_, kept := h.store.Label(value.ID)
	require.True(t, kept)
}
