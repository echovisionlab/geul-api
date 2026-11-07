package emailauthoring_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/emailcontent/emailcontenttest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestCreateEmailTemplateStoresItsDocumentSourceSubjectAndPolicy(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	description := "  Sent once  "

	created, err := h.service.CreateEmailTemplate(h.admin.ctx, emailauthoring.CreateEmailTemplateCommand{
		Key: "welcome_note", Name: " Welcome ", Subject: " Hello ", SourceLocale: "ko", Description: &description,
	})

	require.NoError(t, err)
	require.Equal(t, "Welcome", created.Name)
	require.Equal(t, "Hello", created.Subject)
	require.Equal(t, "Sent once", *created.Description)
	require.True(t, created.IsActive)
	require.Equal(t, "ko", created.Projection.SourceLocale)
	require.NotEmpty(t, created.Projection.Revision)
	stored, ok := h.store.Template(created.ID)
	require.True(t, ok)
	require.NotNil(t, stored.ContentDocumentID)
	source, ok := h.store.Locale(created.ID, "ko")
	require.True(t, ok)
	require.Equal(t, "Hello", *source.Subject)
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditEmailTemplateCreated}, recordActions(h.audit.Records()))
	touch, err := policyv1.EmailTemplate.TouchPolicy(created.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, h.checker.Applied())
}

func TestCreateEmailTemplateRejectsInvalidInputBeforeAnyWrite(t *testing.T) {
	t.Parallel()
	for name, command := range map[string]emailauthoring.CreateEmailTemplateCommand{
		"key":           {Key: "Bad Key", Name: "Name", Subject: "S", SourceLocale: "en"},
		"name":          {Key: "good_key", Name: " ", Subject: "S", SourceLocale: "en"},
		"subject":       {Key: "good_key", Name: "Name", Subject: "", SourceLocale: "en"},
		"source_locale": {Key: "good_key", Name: "Name", Subject: "S", SourceLocale: "xx"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			_, err := h.service.CreateEmailTemplate(h.admin.ctx, command)
			require.Equal(t, connect.CodeInvalidArgument, codeOf(err))
			var domainErr *domainerr.Error
			require.ErrorAs(t, err, &domainErr)
			require.Equal(t, name, domainErr.Field)
			requireRolledBack(t, h.units)
			require.Empty(t, h.checker.Applied())
		})
	}
}

func TestCreateEmailTemplateAnswersATakenKey(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.store.PutTemplate(emailauthoring.Template{Key: "welcome_note", Name: "Existing"}, "Hi")

	_, err := h.service.CreateEmailTemplate(h.admin.ctx, emailauthoring.CreateEmailTemplateCommand{
		Key: "welcome_note", Name: "Second", Subject: "S", SourceLocale: "en",
	})

	require.Equal(t, connect.CodeAlreadyExists, codeOf(err))
	require.EqualError(t, wrapped(err), "already_exists: template with key \"welcome_note\" already exists")
	requireRolledBack(t, h.units)
}

func TestGetEmailTemplateReadsItsSourceSubjectDocumentAndDeliveryRuns(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	template := h.store.PutTemplate(emailauthoring.Template{Key: "welcome_note", Name: "Welcome", SourceLocale: "ko"}, "  Hello  ")
	h.store.PutDeliveryRuns(template.ID, 3)

	read, err := h.service.GetEmailTemplate(h.admin.ctx, emailauthoring.GetEmailTemplateQuery{ID: template.ID})

	require.NoError(t, err)
	require.Equal(t, "Hello", read.Subject)
	require.Equal(t, int32(3), read.DeliveryRunCount)
	require.Equal(t, "ko", read.Projection.SourceLocale)
	_, err = h.service.GetEmailTemplate(h.admin.ctx, emailauthoring.GetEmailTemplateQuery{ID: uuid.NewString()})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
}

func TestListEmailTemplatesPagesWithoutReadingTranslations(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, name := range []string{"b", "a", "c"} {
		template := h.store.PutTemplate(emailauthoring.Template{Key: "key_" + name, Name: name}, "S")
		h.store.PutDeliveryRuns(template.ID, 2)
	}
	h.store.FailOn("LocaleRow", errDown)

	page, err := h.service.ListEmailTemplates(h.admin.ctx, emailauthoring.ListEmailTemplatesQuery{Limit: 2, Offset: 1})

	require.NoError(t, err)
	require.Equal(t, int64(3), page.Total)
	require.Equal(t, []string{"b", "c"}, []string{page.Templates[0].Name, page.Templates[1].Name})
	require.Equal(t, int32(2), page.Templates[0].DeliveryRunCount)
	require.Equal(t, int32(2), page.Page.Limit)
}

func TestUpdateEmailTemplateAuditsOnlyWhatChanged(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	layout := h.store.PutLayout(emailauthoring.Layout{Key: "frame", Name: "Frame"}, "<main>{{content}}</main>")
	template := h.store.PutTemplate(emailauthoring.Template{Key: "welcome_note", Name: "Welcome", IsActive: true, LayoutID: &layout.ID}, "Hi")
	name, sameLayout := "Renamed", layout.ID

	update, err := h.service.UpdateEmailTemplate(h.admin.ctx, emailauthoring.UpdateEmailTemplateCommand{ID: template.ID, Name: &name, LayoutID: &sameLayout})

	require.NoError(t, err)
	require.True(t, update.Changed)
	require.Equal(t, "Renamed", update.Name)
	require.Len(t, h.audit.Records(), 1, "the unchanged layout records no relation change")
	require.Equal(t, []string{"name"}, h.audit.Records()[0].ChangedFields)

	clear := ""
	update, err = h.service.UpdateEmailTemplate(h.admin.ctx, emailauthoring.UpdateEmailTemplateCommand{ID: template.ID, LayoutID: &clear})
	require.NoError(t, err)
	require.Nil(t, update.LayoutID)
	require.Len(t, h.audit.Records(), 2)
	stored, _ := h.store.Template(template.ID)
	require.Nil(t, stored.LayoutID)

	update, err = h.service.UpdateEmailTemplate(h.admin.ctx, emailauthoring.UpdateEmailTemplateCommand{ID: template.ID, Name: &name})
	require.NoError(t, err)
	require.False(t, update.Changed)
	require.Len(t, h.audit.Records(), 2, "an unchanged update records nothing")
}

func TestUpdateEmailTemplateRefusesFrozenMappedOrMissingTargets(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := "welcome"
	mapped := h.store.PutTemplate(emailauthoring.Template{Key: "mapped", Name: "Mapped", IsActive: true, EventKey: &event}, "Hi")
	frozen := h.store.PutTemplate(emailauthoring.Template{Key: "frozen", Name: "Frozen", IsActive: true}, "Hi")
	h.store.Freeze(frozen.ID)
	inactive, missingLayout, name := false, uuid.NewString(), "x"

	_, err := h.service.UpdateEmailTemplate(h.admin.ctx, emailauthoring.UpdateEmailTemplateCommand{ID: mapped.ID, IsActive: &inactive})
	require.EqualError(t, wrapped(err), "failed_precondition: mapped email template must be explicitly unmapped before deactivation")
	_, err = h.service.UpdateEmailTemplate(h.admin.ctx, emailauthoring.UpdateEmailTemplateCommand{ID: frozen.ID, Name: &name})
	require.EqualError(t, wrapped(err), "failed_precondition: email template is frozen by an active delivery run")
	_, err = h.service.UpdateEmailTemplate(h.admin.ctx, emailauthoring.UpdateEmailTemplateCommand{ID: mapped.ID, LayoutID: &missingLayout})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
	_, err = h.service.UpdateEmailTemplate(h.admin.ctx, emailauthoring.UpdateEmailTemplateCommand{ID: uuid.NewString(), Name: &name})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
	require.Empty(t, h.audit.Records())
}

func TestDeleteEmailTemplateDetachesHistoryAndRevokesItsPolicy(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	template := h.store.PutTemplate(emailauthoring.Template{Key: "gone", Name: "Gone"}, "Hi")

	require.NoError(t, h.service.DeleteEmailTemplate(h.admin.ctx, emailauthoring.DeleteEmailTemplateCommand{ID: template.ID}))

	_, ok := h.store.Template(template.ID)
	require.False(t, ok)
	_, ok = h.store.Document(uuid.MustParse(*template.ContentDocumentID))
	require.False(t, ok)
	require.Equal(t, []string{template.ID}, h.store.Detached())
	deletePolicy, err := policyv1.EmailTemplate.DeletePolicy(template.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{deletePolicy}}, h.checker.Applied())
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditEmailTemplateDeleted}, recordActions(h.audit.Records()))
}

func TestDeleteEmailTemplateKeepsAMappedOrFrozenTemplate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := "welcome"
	mapped := h.store.PutTemplate(emailauthoring.Template{Key: "mapped", Name: "Mapped", EventKey: &event}, "Hi")
	frozen := h.store.PutTemplate(emailauthoring.Template{Key: "frozen", Name: "Frozen"}, "Hi")
	h.store.Freeze(frozen.ID)

	err := h.service.DeleteEmailTemplate(h.admin.ctx, emailauthoring.DeleteEmailTemplateCommand{ID: mapped.ID})
	require.EqualError(t, wrapped(err), "failed_precondition: email template must be explicitly unmapped before deletion")
	err = h.service.DeleteEmailTemplate(h.admin.ctx, emailauthoring.DeleteEmailTemplateCommand{ID: frozen.ID})
	require.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
	_, ok := h.store.Template(frozen.ID)
	require.True(t, ok)
	require.Empty(t, h.store.Detached())
	require.Empty(t, h.checker.Applied())
}

func TestDeleteEmailTemplateRollsBackWhenTheAuditFails(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	template := h.store.PutTemplate(emailauthoring.Template{Key: "kept", Name: "Kept"}, "Hi")
	h.audit.Err = errDown

	err := h.service.DeleteEmailTemplate(h.admin.ctx, emailauthoring.DeleteEmailTemplateCommand{ID: template.ID})

	require.ErrorIs(t, err, errDown)
	require.Equal(t, mutation.StepRollback, lastStep(h.units))
	_, ok := h.store.Template(template.ID)
	require.True(t, ok)
	require.Empty(t, h.store.Detached())
}

func TestUpdateEventMappingMovesTheEventAndAuditsOnlyAMove(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := "welcome"
	current := h.store.PutTemplate(emailauthoring.Template{Key: "current", Name: "Current", IsActive: true, EventKey: &event}, "Hi")
	next := h.store.PutTemplate(emailauthoring.Template{Key: "next", Name: "Next", IsActive: true}, "Hi")

	require.NoError(t, h.service.UpdateEventMapping(h.admin.ctx, emailauthoring.UpdateEventMappingCommand{Event: "welcome", TemplateID: &next.ID}))

	mappings, err := h.service.GetEventMappings(h.admin.ctx)
	require.NoError(t, err)
	require.Equal(t, []emailauthoring.EventMapping{{Event: "welcome", TemplateID: next.ID, TemplateName: "Next"}}, mappings)
	stored, _ := h.store.Template(current.ID)
	require.Nil(t, stored.EventKey)
	require.Len(t, h.audit.Records(), 1)

	require.NoError(t, h.service.UpdateEventMapping(h.admin.ctx, emailauthoring.UpdateEventMappingCommand{Event: "welcome", TemplateID: &next.ID}))
	require.Len(t, h.audit.Records(), 1, "the same mapping records nothing")

	require.NoError(t, h.service.UpdateEventMapping(h.admin.ctx, emailauthoring.UpdateEventMappingCommand{Event: "welcome"}))
	mappings, err = h.service.GetEventMappings(h.admin.ctx)
	require.NoError(t, err)
	require.Empty(t, mappings)
	require.Len(t, h.audit.Records(), 2)
}

func TestUpdateEventMappingRejectsUnknownEventsAndInactiveOrFrozenTemplates(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	inactive := h.store.PutTemplate(emailauthoring.Template{Key: "inactive", Name: "Inactive"}, "Hi")
	frozen := h.store.PutTemplate(emailauthoring.Template{Key: "frozen", Name: "Frozen", IsActive: true}, "Hi")
	h.store.Freeze(frozen.ID)
	missing := uuid.NewString()

	err := h.service.UpdateEventMapping(h.admin.ctx, emailauthoring.UpdateEventMappingCommand{Event: "custom", TemplateID: &inactive.ID})
	require.EqualError(t, wrapped(err), "invalid_argument: event: unsupported email event")
	err = h.service.UpdateEventMapping(h.admin.ctx, emailauthoring.UpdateEventMappingCommand{Event: "welcome", TemplateID: &inactive.ID})
	require.EqualError(t, wrapped(err), "failed_precondition: email template must be active before it can be mapped")
	err = h.service.UpdateEventMapping(h.admin.ctx, emailauthoring.UpdateEventMappingCommand{Event: "welcome", TemplateID: &frozen.ID})
	require.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
	err = h.service.UpdateEventMapping(h.admin.ctx, emailauthoring.UpdateEventMappingCommand{Event: "welcome", TemplateID: &missing})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
	require.Empty(t, h.audit.Records())
}

func TestSendTestEmailQueuesTheDirectTemplateForTheAdministrator(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	template := h.store.PutTemplate(emailauthoring.Template{Key: "welcome_note", Name: "Welcome"}, "Hi")
	locale := " pt "

	require.NoError(t, h.service.SendTestEmail(h.admin.ctx, emailauthoring.SendTestEmailCommand{ID: template.ID, Email: "admin@example.com", Locale: &locale}))

	require.Len(t, h.queue.queued, 1)
	queued := h.queue.queued[0]
	require.Equal(t, template.ID, queued.TemplateID)
	require.Equal(t, "admin@example.com", queued.Data["recipient_email"])
	require.Equal(t, h.admin.memberID, queued.ActorMemberID)
	require.Equal(t, "pt-BR", *queued.Locale)
}

func TestSendTestEmailRefusesWhatTheMailAdapterCannotDeliver(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	template := h.store.PutTemplate(emailauthoring.Template{Key: "welcome_note", Name: "Welcome"}, "Hi")

	err := h.service.SendTestEmail(h.admin.ctx, emailauthoring.SendTestEmailCommand{ID: template.ID, Email: "not-an-address"})
	require.Equal(t, connect.CodeInvalidArgument, codeOf(err))
	err = h.service.SendTestEmail(h.admin.ctx, emailauthoring.SendTestEmailCommand{ID: uuid.NewString(), Email: "admin@example.com"})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
	h.store.TestSendReadiness = errDown
	err = h.service.SendTestEmail(h.admin.ctx, emailauthoring.SendTestEmailCommand{ID: template.ID, Email: "admin@example.com"})
	require.ErrorIs(t, err, errDown)
	h.queue.err = errDown
	h.store.TestSendReadiness = nil
	err = h.service.SendTestEmail(h.admin.ctx, emailauthoring.SendTestEmailCommand{ID: template.ID, Email: "admin@example.com"})
	require.EqualError(t, err, "failed to queue test email: down")
}

func TestPreviewEmailTemplateRendersTheLocaleInsideItsLayout(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	layout := h.store.PutLayout(emailauthoring.Layout{Key: "frame", Name: "Frame"}, "<main>{{content}}</main>")
	template := h.store.PutTemplate(emailauthoring.Template{Key: "welcome_note", Name: "Welcome", LayoutID: &layout.ID}, "Hello {{name}}")
	html := "<p>Body {{custom_value}}</p>"
	h.store.PutLocale(template.ID, emailcontenttest.Locale{
		LocaleMetadata: emailcontent.LocaleMetadata{Locale: "ko", Subject: ptr("안녕 {{name}}")}, HTML: &html,
	})
	locale := "ko"

	preview, err := h.service.PreviewEmailTemplate(h.admin.ctx, emailauthoring.PreviewEmailTemplateQuery{ID: template.ID, Locale: &locale})

	require.NoError(t, err)
	require.Equal(t, "안녕 John Doe", preview.Subject)
	require.Contains(t, preview.HTML, "<main>")
	require.Contains(t, preview.HTML, "Body sample_custom_value")
	_, err = h.service.PreviewEmailTemplate(h.admin.ctx, emailauthoring.PreviewEmailTemplateQuery{ID: uuid.NewString()})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
}

func ptr[T any](value T) *T { return &value }
