package emailauthoring_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestCreateEmailLayoutStoresItsCanonicalSource(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	created, err := h.service.CreateEmailLayout(h.admin.ctx, emailauthoring.CreateEmailLayoutCommand{
		Name: " Frame ", Key: "frame", HTMLContent: "<main><h1>Hello</h1>{{ content }}</main>", SourceLocale: "en",
	})

	require.NoError(t, err)
	require.Equal(t, "Frame", created.Name)
	require.Contains(t, created.HTMLContent, "{{content}}")
	source, ok := h.store.LayoutTranslation(created.ID, "en")
	require.True(t, ok)
	require.Equal(t, created.HTMLContent, *source.ContentHTML)
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditEmailLayoutCreated}, recordActions(h.audit.Records()))

	_, err = h.service.CreateEmailLayout(h.admin.ctx, emailauthoring.CreateEmailLayoutCommand{
		Name: "Other", Key: "frame", HTMLContent: "<main>{{content}}</main>", SourceLocale: "en",
	})
	require.EqualError(t, wrapped(err), "already_exists: email layout with key 'frame' already exists")
	requireRolledBack(t, h.units)
}

func TestCreateEmailLayoutRejectsLayoutsWithoutContent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, err := h.service.CreateEmailLayout(h.admin.ctx, emailauthoring.CreateEmailLayoutCommand{
		Name: "Frame", Key: "frame", HTMLContent: "<main></main>", SourceLocale: "en",
	})
	require.Equal(t, connect.CodeInvalidArgument, codeOf(err))
	_, err = h.service.CreateEmailLayout(h.admin.ctx, emailauthoring.CreateEmailLayoutCommand{
		Name: "Frame", Key: "frame", HTMLContent: "<main>{{content}}</main>", SourceLocale: "EN",
	})
	require.Equal(t, connect.CodeInvalidArgument, codeOf(err))
}

func TestGetAndListEmailLayoutsCountTheirReferences(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	layout := h.store.PutLayout(emailauthoring.Layout{Key: "frame", Name: "Frame"}, "<main>{{content}}</main>")
	h.store.PutTemplate(emailauthoring.Template{Key: "uses_frame", Name: "Uses", LayoutID: &layout.ID}, "Hi")
	h.store.PutLayoutReferences(layout.ID, emailauthoring.LayoutExternalReferenceCounts{Campaigns: 6, DeliveryRuns: 8})

	read, err := h.service.GetEmailLayout(h.admin.ctx, emailauthoring.GetEmailLayoutQuery{ID: layout.ID})
	require.NoError(t, err)
	require.Equal(t, "<main>{{content}}</main>", read.HTMLContent)
	require.Equal(t, [3]int32{6, 1, 8}, [3]int32{read.CampaignCount, read.TemplateCount, read.DeliveryRunCount})

	page, err := h.service.ListEmailLayouts(h.admin.ctx, emailauthoring.ListEmailLayoutsQuery{})
	require.NoError(t, err)
	require.Len(t, page.Layouts, 1)
	require.Equal(t, int32(6), page.Layouts[0].CampaignCount)
	require.Equal(t, "<main>{{content}}</main>", page.Layouts[0].HTMLContent)

	_, err = h.service.GetEmailLayout(h.admin.ctx, emailauthoring.GetEmailLayoutQuery{ID: uuid.NewString()})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
}

func TestListEmailLayoutsFailsClosedOnALayoutWithoutItsSource(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.store.PutLayout(emailauthoring.Layout{Key: "whole", Name: "Whole"}, "<main>{{content}}</main>")
	broken := h.store.PutLayout(emailauthoring.Layout{Key: "broken", Name: "Broken"}, "<main>{{content}}</main>")
	_, err := h.store.DeleteLayoutTranslation(t.Context(), broken.ID, "en")
	require.NoError(t, err)

	_, err = h.service.ListEmailLayouts(h.admin.ctx, emailauthoring.ListEmailLayoutsQuery{})

	require.Equal(t, connect.CodeInternal, codeOf(err))
	require.EqualError(t, err, "email layout "+broken.ID+" has no canonical source translation")
}

func TestUpdateEmailLayoutIsFrozenOnlyWhileADeliveryRendersIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	layout := h.store.PutLayout(emailauthoring.Layout{Key: "frame", Name: "Frame"}, "<main>{{content}}</main>")
	h.store.PutLayout(emailauthoring.Layout{Key: "taken", Name: "Taken"}, "<main>{{content}}</main>")
	name, taken := "Renamed", "taken"

	h.store.Freeze(layout.ID)
	_, err := h.service.UpdateEmailLayout(h.admin.ctx, emailauthoring.UpdateEmailLayoutCommand{ID: layout.ID, Name: &name})
	require.EqualError(t, wrapped(err), "failed_precondition: email layout is frozen by an active delivery run")

	h = newHarness(t)
	layout = h.store.PutLayout(emailauthoring.Layout{Key: "frame", Name: "Frame"}, "<main>{{content}}</main>")
	h.store.PutLayout(emailauthoring.Layout{Key: "taken", Name: "Taken"}, "<main>{{content}}</main>")
	updated, err := h.service.UpdateEmailLayout(h.admin.ctx, emailauthoring.UpdateEmailLayoutCommand{ID: layout.ID, Name: &name})
	require.NoError(t, err)
	require.Equal(t, "Renamed", updated.Name)
	require.Equal(t, "<main>{{content}}</main>", updated.HTMLContent)
	require.Len(t, h.audit.Records(), 1)
	require.Equal(t, []string{"name"}, h.audit.Records()[0].ChangedFields)

	_, err = h.service.UpdateEmailLayout(h.admin.ctx, emailauthoring.UpdateEmailLayoutCommand{ID: layout.ID, Key: &taken})
	require.EqualError(t, wrapped(err), "already_exists: email layout with key 'taken' already exists")
	_, err = h.service.UpdateEmailLayout(h.admin.ctx, emailauthoring.UpdateEmailLayoutCommand{ID: layout.ID, Name: &name})
	require.NoError(t, err)
	require.Len(t, h.audit.Records(), 1, "an unchanged update records nothing")
}

func TestDeleteEmailLayoutRequiresNoCampaignOrTemplate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	used := h.store.PutLayout(emailauthoring.Layout{Key: "used", Name: "Used"}, "<main>{{content}}</main>")
	h.store.PutLayoutReferences(used.ID, emailauthoring.LayoutExternalReferenceCounts{Campaigns: 1})
	free := h.store.PutLayout(emailauthoring.Layout{Key: "free", Name: "Free"}, "<main>{{content}}</main>")

	err := h.service.DeleteEmailLayout(h.admin.ctx, emailauthoring.DeleteEmailLayoutCommand{ID: used.ID})
	require.EqualError(t, wrapped(err), "failed_precondition: email layout must be unreferenced by campaigns and templates before deletion")
	require.NoError(t, h.service.DeleteEmailLayout(h.admin.ctx, emailauthoring.DeleteEmailLayoutCommand{ID: free.ID}))

	_, ok := h.store.Layout(free.ID)
	require.False(t, ok)
	require.Equal(t, []string{free.ID}, h.store.Detached())
	_, ok = h.store.Layout(used.ID)
	require.True(t, ok)
}

func TestPreviewEmailLayoutFallsBackToTheSourceForAMissingLocale(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	layout := h.store.PutLayout(emailauthoring.Layout{Key: "frame", Name: "Frame"}, "<main>source {{content}}</main>")
	locale := "ja"

	html, err := h.service.PreviewEmailLayout(h.admin.ctx, emailauthoring.PreviewEmailLayoutQuery{ID: layout.ID, Locale: &locale})

	require.NoError(t, err)
	require.Contains(t, html, "source <p>This is sample email content")
}

func TestPreviewEmailLayoutContentValidatesBeforeRendering(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	sample := "<p>Body</p>"

	invalid, err := h.service.PreviewEmailLayoutContent(h.admin.ctx, emailauthoring.PreviewEmailLayoutContentQuery{HTMLContent: "<main></main>"})
	require.NoError(t, err)
	require.False(t, invalid.Valid)
	require.NotEmpty(t, invalid.Errors)
	require.Equal(t, email.ValidateLayoutHTMLContent("<main></main>")[0].Code, invalid.Errors[0].Code)

	valid, err := h.service.PreviewEmailLayoutContent(h.admin.ctx, emailauthoring.PreviewEmailLayoutContentQuery{HTMLContent: "<main>{{ content }}</main>", SampleContent: &sample})
	require.NoError(t, err)
	require.True(t, valid.Valid)
	require.Equal(t, "<main><p>Body</p></main>", valid.HTML)
}
