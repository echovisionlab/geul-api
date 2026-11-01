package series_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/series"
	"github.com/echovisionlab/geul-api/internal/series/seriestest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const (
	aiBlock   core.BlockID    = "series"
	aiPosts   core.RelationID = "posts"
	aiTitle   core.FieldID    = "title"
	aiSlug    core.FieldID    = "slug"
	aiStatus  core.FieldID    = "status"
	aiSummary core.FieldID    = "summary"
)

// aiFixture is one Series with a source copy and two Posts, served through
// the shared AI document application.
type aiFixture struct {
	*fixture
	application *core.Service
	seriesID    string
	postIDs     []string
	identity    core.DocumentIdentity
}

func newAIFixture(t *testing.T) aiFixture {
	t.Helper()
	f := newFixture(t)
	root := f.store.SeedSeries(series.Series{Title: "Source title", Description: text("Source summary"), Slug: "post-series"})
	postIDs := []string{uuid.NewString(), uuid.NewString()}
	for index, postID := range postIDs {
		f.store.SeedPost(seriestest.Post{SeriesPost: series.SeriesPost{ID: postID, SeriesOrder: intPointer(index)}, SeriesID: &root.ID})
	}
	application, err := core.NewService(f.service)
	require.NoError(t, err)
	return aiFixture{
		fixture: f, application: application, seriesID: root.ID, postIDs: postIDs,
		identity: core.DocumentIdentity{Domain: core.DomainPostSeries, Reference: core.DocumentReference(root.ID)},
	}
}

func (a aiFixture) load(t *testing.T, locale core.Locale) core.Document {
	t.Helper()
	document, err := a.service.Load(a.ctx(t), a.identity, locale)
	require.NoError(t, err)
	return document
}

func (a aiFixture) request(locale core.Locale, document core.Document, operations ...core.Operation) core.ApplyRequest {
	return core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPostSeries, Document: a.identity.Reference, Locale: locale,
		ExpectedDocumentRevision: document.DocumentRevision, ExpectedTargetRevision: document.TargetRevision,
		Operations: operations,
	}
}

func localizedText(t *testing.T, document core.Document, field core.FieldID) string {
	t.Helper()
	for _, value := range document.Nodes[0].Localized {
		if value.ID == field {
			require.Equal(t, core.ValueKindText, value.Value.Kind)
			return value.Value.Text
		}
	}
	t.Fatalf("localized field %q is missing", field)
	return ""
}

func TestPostSeriesAIDocumentProjectsMetadataPostsAndExplicitEmpty(t *testing.T) {
	a := newAIFixture(t)

	source := a.load(t, "en")
	require.True(t, source.LocaleExists)
	require.Equal(t, core.LocaleRoleSource, source.Role())
	require.Equal(t, aiBlock, source.Nodes[0].ID)
	require.Equal(t, "Source title", localizedText(t, source, aiTitle))
	require.Equal(t, a.postIDs[0], string(source.Nodes[0].Relations[0].Items[0].ID))
	require.Equal(t, a.postIDs[1], string(source.Nodes[0].Relations[0].Items[1].ID))

	missing := a.load(t, "ko")
	require.False(t, missing.LocaleExists)
	require.Empty(t, missing.Nodes[0].Localized)
	require.Nil(t, missing.TargetRevision)

	a.store.SeedTranslation(series.Translation{SeriesID: a.seriesID, Locale: "ko", Title: text(""), CreatedAt: contractTime, UpdatedAt: contractTime})
	existing := a.load(t, "ko")
	require.True(t, existing.LocaleExists)
	require.Equal(t, "", localizedText(t, existing, aiTitle))
	require.Equal(t, missing.DocumentRevision, existing.DocumentRevision)
	require.NotNil(t, existing.TargetRevision)
}

func TestPostSeriesAIDocumentImplicitTargetCreateExactCASAndDelete(t *testing.T) {
	a := newAIFixture(t)
	missing := a.load(t, "ko")

	request := a.request("ko", missing, core.SetFieldOperation(aiBlock, aiTitle, core.Text("")))
	applied, err := a.application.Apply(a.ctx(t), request)
	require.NoError(t, err)
	require.True(t, applied.Changed)
	require.Equal(t, missing.DocumentRevision, applied.DocumentRevision)
	require.NotNil(t, applied.TargetRevision)
	require.Len(t, applied.Changes, 1)
	require.Equal(t, []string{"field:series/title"}, applied.Changes[0].AffectedHandles)
	target, ok := a.store.StoredTranslation(a.seriesID, "ko")
	require.True(t, ok)
	require.Equal(t, text(""), target.Title)

	_, err = a.application.Apply(a.ctx(t), request)
	var conflict *core.ConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, core.ConflictTargetRevision, conflict.Conflict.Code)
	require.Equal(t, *applied.TargetRevision, *conflict.Conflict.CurrentTargetRevision)

	current := core.Document{DocumentRevision: applied.DocumentRevision, TargetRevision: applied.TargetRevision}
	validation, err := a.application.Validate(a.ctx(t), a.request("ko", current, core.UnsetFieldOperation(aiBlock, aiTitle)))
	require.NoError(t, err)
	require.Len(t, validation.Issues, 1)
	require.Equal(t, core.IssueTargetFieldForbidden, validation.Issues[0].Code)

	deleted, err := a.application.Apply(a.ctx(t), a.request("ko", current, core.DeleteTranslationOperation()))
	require.NoError(t, err)
	require.True(t, deleted.Changed)
	require.Len(t, deleted.Changes, 1)
	require.Equal(t, []string{"translation:ko"}, deleted.Changes[0].AffectedHandles)
	require.Nil(t, deleted.TargetRevision)
	_, ok = a.store.StoredTranslation(a.seriesID, "ko")
	require.False(t, ok)
	require.Len(t, a.store.Signals(), 2, "each changed apply publishes its content signal")
	require.True(t, a.store.Signals()[1].Deleted)

	created, err := a.application.Apply(a.ctx(t), a.request("ko", core.Document{DocumentRevision: deleted.DocumentRevision}, core.CreateTranslationOperation()))
	require.NoError(t, err)
	require.True(t, created.Changed)
	require.NotNil(t, created.TargetRevision)
	target, _ = a.store.StoredTranslation(a.seriesID, "ko")
	require.Nil(t, target.Title)
	require.Nil(t, target.Summary)
}

func TestPostSeriesAIDocumentTargetMutationIsLocaleExactAndSourceInvalidatesEveryTarget(t *testing.T) {
	a := newAIFixture(t)
	for _, locale := range []string{"ko", "ja"} {
		a.store.SeedTranslation(series.Translation{SeriesID: a.seriesID, Locale: locale, Title: text(locale), CreatedAt: contractTime, UpdatedAt: contractTime})
	}
	source, ko, ja := a.load(t, "en"), a.load(t, "ko"), a.load(t, "ja")
	require.Equal(t, source.DocumentRevision, ko.DocumentRevision)

	koUpdated, err := a.application.Apply(a.ctx(t), a.request("ko", ko, core.SetFieldOperation(aiBlock, aiTitle, core.Text("새 한국어"))))
	require.NoError(t, err)
	require.Equal(t, ko.DocumentRevision, koUpdated.DocumentRevision)
	require.NotEqual(t, *ko.TargetRevision, *koUpdated.TargetRevision)
	require.Equal(t, *ja.TargetRevision, *a.load(t, "ja").TargetRevision)

	jaRow, _ := a.store.StoredTranslation(a.seriesID, "ja")
	sourceUpdated, err := a.application.Apply(a.ctx(t), a.request("en", source, core.SetFieldOperation(aiBlock, aiTitle, core.Text("Updated source"))))
	require.NoError(t, err)
	require.NotEqual(t, source.DocumentRevision, sourceUpdated.DocumentRevision)
	jaAfter := a.load(t, "ja")
	require.Equal(t, sourceUpdated.DocumentRevision, jaAfter.DocumentRevision)
	require.NotEqual(t, *ja.TargetRevision, *jaAfter.TargetRevision)
	jaRowAfter, _ := a.store.StoredTranslation(a.seriesID, "ja")
	require.Equal(t, jaRow.UpdatedAt, jaRowAfter.UpdatedAt, "a source change invalidates target tokens without rewriting target rows")
}

func TestPostSeriesAIDocumentSourceConstraintsAndStructuralOrder(t *testing.T) {
	a := newAIFixture(t)
	source := a.load(t, "en")

	validation, err := a.application.Validate(a.ctx(t), a.request("en", source, core.SetFieldOperation(aiBlock, aiTitle, core.Text("   "))))
	require.NoError(t, err)
	require.Len(t, validation.Issues, 1)
	require.Equal(t, core.IssueValueKindMismatch, validation.Issues[0].Code)

	moved, err := a.application.Apply(a.ctx(t), a.request("en", source, core.MoveRelationItemOperation(
		aiBlock, aiPosts, core.RelationItemID(a.postIDs[1]), aiBlock, aiPosts, "",
	)))
	require.NoError(t, err)
	require.True(t, moved.Changed)
	require.Equal(t, []string{a.postIDs[1], a.postIDs[0]}, a.store.Order(a.seriesID))

	updated, err := a.application.Apply(a.ctx(t), a.request("en", core.Document{DocumentRevision: moved.DocumentRevision}, core.SetFieldOperation(aiBlock, aiTitle, core.Text("Next title"))))
	require.NoError(t, err)
	require.True(t, updated.Changed)
	require.Equal(t, []seriestest.OGRequest{{SeriesID: a.seriesID, Locale: "en", Reason: "series_ai_document_title_updated"}}, a.store.OGRequests())
}

func TestPostSeriesAIDocumentRepeatedMetadataAssignmentsUseFinalValue(t *testing.T) {
	for _, test := range []struct {
		name    string
		field   core.FieldID
		values  []string
		want    string
		changed bool
	}{
		{"status reset", aiStatus, []string{"SERIES_STATUS_PUBLISHED", "SERIES_STATUS_DRAFT"}, "SERIES_STATUS_DRAFT", false},
		{"status repeated", aiStatus, []string{"SERIES_STATUS_PUBLISHED", "SERIES_STATUS_PUBLISHED"}, "SERIES_STATUS_PUBLISHED", true},
		{"status final assignment", aiStatus, []string{"SERIES_STATUS_PUBLISHED", "SERIES_STATUS_DRAFT", "SERIES_STATUS_PUBLISHED"}, "SERIES_STATUS_PUBLISHED", true},
		{"slug reset", aiSlug, []string{"changed-series", "post-series"}, "post-series", false},
		{"slug repeated", aiSlug, []string{"changed-series", "changed-series"}, "changed-series", true},
		{"slug final assignment", aiSlug, []string{"changed-series", "post-series", "final-series"}, "final-series", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := newAIFixture(t)
			before := a.load(t, "en")
			operations := make([]core.Operation, 0, len(test.values))
			for _, value := range test.values {
				operations = append(operations, core.SetFieldOperation(aiBlock, test.field, core.Text(value)))
			}
			result, err := a.application.Apply(a.ctx(t), a.request("en", before, operations...))
			require.NoError(t, err)
			require.Equal(t, test.changed, result.Changed)
			require.Len(t, result.Normalized, len(operations))
			stored, _ := a.store.Stored(a.seriesID)
			if test.field == aiStatus {
				require.Equal(t, test.want, string(stored.Status))
			} else {
				require.Equal(t, test.want, stored.Slug)
			}
			if !test.changed {
				require.Equal(t, before.DocumentRevision, result.DocumentRevision)
				require.Empty(t, result.Changes)
				require.Empty(t, a.store.MenuChanges())
				require.Empty(t, a.audit.Records())
			}
		})
	}
}

func TestPostSeriesAIDocumentStatusResetKeepsOtherFieldChange(t *testing.T) {
	a := newAIFixture(t)
	result, err := a.application.Apply(a.ctx(t), a.request("en", a.load(t, "en"),
		core.SetFieldOperation(aiBlock, aiStatus, core.Text("SERIES_STATUS_PUBLISHED")),
		core.SetFieldOperation(aiBlock, aiTitle, core.Text("Changed title")),
		core.SetFieldOperation(aiBlock, aiStatus, core.Text("SERIES_STATUS_DRAFT")),
	))
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Len(t, result.Changes, 1)
	require.Equal(t, 1, result.Changes[0].Operation)
	stored, _ := a.store.Stored(a.seriesID)
	require.Equal(t, series.StatusDraft, stored.Status)
	require.Equal(t, "Changed title", localizedText(t, a.load(t, "en"), aiTitle))
	records := a.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, []string{"source_copy"}, records[0].ChangedFields)
}

func TestPostSeriesAIDocumentRenamesMenuTargets(t *testing.T) {
	a := newAIFixture(t)
	result, err := a.application.Apply(a.ctx(t), a.request("en", a.load(t, "en"), core.SetFieldOperation(aiBlock, aiSlug, core.Text("renamed"))))
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, []seriestest.MenuChange{{SeriesID: a.seriesID, OldSlug: "post-series", NewSlug: "renamed"}}, a.store.MenuChanges())
	records := a.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, []string{"slug"}, records[0].ChangedFields)
}

func TestPostSeriesExactMutationUsesOneDecisionAndValidateRollsBack(t *testing.T) {
	a := newAIFixture(t)
	loaded := a.load(t, "en")
	request := a.request("en", loaded, core.SetFieldOperation(aiBlock, aiTitle, core.Text("Validated title")))
	checks := len(a.checker.Checks())

	validation, err := a.application.Validate(a.ctx(t), request)
	require.NoError(t, err)
	require.True(t, validation.Valid())
	stored, _ := a.store.StoredTranslation(a.seriesID, "en")
	require.Equal(t, text("Source title"), stored.Title, "Validate rolls the owning transaction back")
	require.Empty(t, a.audit.Records())

	result, err := a.application.Apply(a.ctx(t), request)
	require.NoError(t, err)
	require.True(t, result.Changed)
	edit := authztest.Of(a.identityID, must(policyv1.PostSeries.Edit(a.seriesID)), nil)
	require.Equal(t, []authztest.Check{edit, edit}, a.checker.Checks()[checks:], "Validate and Apply each make one exact decision")
	stored, _ = a.store.StoredTranslation(a.seriesID, "en")
	require.Equal(t, text("Validated title"), stored.Title)
}

func TestPostSeriesAIDocumentHidesTheSeriesFromACallerWithoutTheAction(t *testing.T) {
	a := newAIFixture(t)
	loaded := a.load(t, "en")
	a.checker.Deny(policyv1.PostSeries.Edit(a.seriesID))
	_, err := a.application.Apply(a.ctx(t), a.request("en", loaded, core.SetFieldOperation(aiBlock, aiSummary, core.Text("Hidden"))))
	require.Error(t, err)
	require.Contains(t, err.Error(), "series not found")
	stored, _ := a.store.StoredTranslation(a.seriesID, "en")
	require.Equal(t, text("Source summary"), stored.Summary)
}

func TestPostSeriesAIDocumentInsertsAPostAndAdvancesTheDocumentOnce(t *testing.T) {
	a := newAIFixture(t)
	other := a.store.SeedSeries(series.Series{Title: "Other"})
	loose := a.store.SeedPost(seriestest.Post{}).ID
	moved := a.store.SeedPost(seriestest.Post{SeriesID: &other.ID, SeriesPost: series.SeriesPost{SeriesOrder: intPointer(0)}}).ID
	otherRevision := a.store.Revision(other.ID)
	before := a.load(t, "en")

	result, err := a.application.Apply(a.ctx(t), a.request("en", before,
		core.InsertRelationItemOperation(aiBlock, aiPosts, core.RelationItemID(loose), "post", core.RelationItemID(a.postIDs[0])),
		core.InsertRelationItemOperation(aiBlock, aiPosts, core.RelationItemID(moved), "post", ""),
	))

	require.NoError(t, err)
	require.True(t, result.Changed)
	require.NotEqual(t, before.DocumentRevision, result.DocumentRevision)
	require.Equal(t, string(result.DocumentRevision), a.store.Revision(a.seriesID))
	require.Equal(t, []string{moved, a.postIDs[0], loose, a.postIDs[1]}, a.store.Order(a.seriesID))
	require.Empty(t, a.store.Order(other.ID))
	require.NotEqual(t, otherRevision, a.store.Revision(other.ID), "the Series the Post left advances too")
}
