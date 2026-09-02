package series

import (
	"context"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type allowSeriesAIDocumentAccess struct{}

func (allowSeriesAIDocumentAccess) RequirePermissionAndLock(
	context.Context,
	*gorm.DB,
	string,
	seriesAction,
) error {
	return nil
}

type recordingSeriesAIDocumentAccess struct {
	actions []policyv1.Can
	err     error
}

func (a *recordingSeriesAIDocumentAccess) RequirePermissionAndLock(
	_ context.Context,
	_ *gorm.DB,
	seriesID string,
	action seriesAction,
) error {
	can, err := action(seriesID)
	if err != nil {
		return err
	}
	a.actions = append(a.actions, can)
	return a.err
}

type seriesAIDocumentMenu struct{}

func (seriesAIDocumentMenu) UpdateSlug(context.Context, *gorm.DB, string, string, string, string) error {
	return nil
}
func (seriesAIDocumentMenu) Remove(context.Context, *gorm.DB, string, string, string) error {
	return nil
}

type seriesAIDocumentPostAccess struct{}

func (seriesAIDocumentPostAccess) PostSourceTitleSQL() string { return "''" }
func (seriesAIDocumentPostAccess) RequireLockedEdit(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error {
	return nil
}

type seriesAIDocumentOG struct{ locales []string }

func (o *seriesAIDocumentOG) RequestCurrent(_ context.Context, _ *gorm.DB, _ string, locale string, _ bool, _ string) (*string, error) {
	o.locales = append(o.locales, locale)
	return nil, nil
}

func TestPostSeriesAIDocumentProjectsStableMetadataPostsAndExplicitEmpty(t *testing.T) {
	service, db, seriesID, postIDs, _ := newSeriesAIDocumentFixture(t)
	identity := core.DocumentIdentity{Domain: core.DomainPostSeries, Reference: core.DocumentReference(seriesID)}

	source, err := service.Load(t.Context(), identity, "en")
	require.NoError(t, err)
	require.True(t, source.LocaleExists)
	require.Equal(t, core.LocaleRoleSource, source.Role())
	require.Len(t, source.Nodes, 1)
	require.Equal(t, postSeriesAIDocumentBlock, source.Nodes[0].ID)
	require.Equal(t, "Source title", requireAIDocumentLocalizedText(t, source, postSeriesAIFieldTitle))
	require.Equal(t, postIDs[0], string(source.Nodes[0].Relations[0].Items[0].ID))
	require.Equal(t, postIDs[1], string(source.Nodes[0].Relations[0].Items[1].ID))

	missing, err := service.Load(t.Context(), identity, "ko")
	require.NoError(t, err)
	require.False(t, missing.LocaleExists)
	require.Equal(t, core.LocaleRoleNonSource, missing.Role())
	require.Empty(t, missing.Nodes[0].Localized)
	require.NotEmpty(t, missing.DocumentRevision)
	require.Nil(t, missing.TargetRevision)

	empty := ""
	now := time.Date(2026, 8, 23, 5, 0, 0, 0, time.UTC)
	require.NoError(t, db.Table("series_translation").Create(&model.SeriesTranslation{
		EntityID: seriesID, Locale: "ko", Title: &empty, CreatedAt: now, UpdatedAt: now,
	}).Error)
	existing, err := service.Load(t.Context(), identity, "ko")
	require.NoError(t, err)
	require.True(t, existing.LocaleExists)
	require.Equal(t, "", requireAIDocumentLocalizedText(t, existing, postSeriesAIFieldTitle))
	require.Equal(t, missing.DocumentRevision, existing.DocumentRevision)
	require.NotNil(t, existing.TargetRevision)
}

func TestPostSeriesAIDocumentImplicitTargetCreateExactCASAndDelete(t *testing.T) {
	port, db, seriesID, _, _ := newSeriesAIDocumentFixture(t)
	application, err := core.NewService(port)
	require.NoError(t, err)
	identity := core.DocumentIdentity{Domain: core.DomainPostSeries, Reference: core.DocumentReference(seriesID)}
	missing, err := port.Load(t.Context(), identity, "ko")
	require.NoError(t, err)

	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPostSeries,
		Document: identity.Reference, Locale: "ko", ExpectedDocumentRevision: missing.DocumentRevision,
		Operations: []core.Operation{core.SetFieldOperation(postSeriesAIDocumentBlock, postSeriesAIFieldTitle, core.Text(""))},
	}
	applied, err := application.Apply(t.Context(), request)
	require.NoError(t, err)
	require.True(t, applied.Changed)
	require.Equal(t, missing.DocumentRevision, applied.DocumentRevision)
	require.NotNil(t, applied.TargetRevision)

	var target model.SeriesTranslation
	require.NoError(t, db.Table("series_translation").Where("entity_id = ? AND locale = 'ko'", seriesID).Take(&target).Error)
	require.NotNil(t, target.Title)
	require.Equal(t, "", *target.Title)

	_, err = application.Apply(t.Context(), request)
	var conflict *core.ConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, core.ConflictTargetRevision, conflict.Conflict.Code)
	require.Equal(t, applied.DocumentRevision, conflict.Conflict.CurrentDocumentRevision)
	require.Equal(t, *applied.TargetRevision, *conflict.Conflict.CurrentTargetRevision)

	unsetRequest := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPostSeries,
		Document: identity.Reference, Locale: "ko", ExpectedDocumentRevision: applied.DocumentRevision,
		ExpectedTargetRevision: applied.TargetRevision,
		Operations:             []core.Operation{core.UnsetFieldOperation(postSeriesAIDocumentBlock, postSeriesAIFieldTitle)},
	}
	validation, err := application.Validate(t.Context(), unsetRequest)
	require.NoError(t, err)
	require.Len(t, validation.Issues, 1)
	require.Equal(t, core.IssueTargetFieldForbidden, validation.Issues[0].Code)
	require.NoError(t, db.Table("series_translation").Where("entity_id = ? AND locale = 'ko'", seriesID).Take(&target).Error)
	require.NotNil(t, target.Title)
	require.Equal(t, "", *target.Title, "target unset must not remove the locale unit")

	deleteRequest := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPostSeries,
		Document: identity.Reference, Locale: "ko", ExpectedDocumentRevision: applied.DocumentRevision,
		ExpectedTargetRevision: applied.TargetRevision,
		Operations:             []core.Operation{core.DeleteTranslationOperation()},
	}
	deleted, err := application.Apply(t.Context(), deleteRequest)
	require.NoError(t, err)
	require.True(t, deleted.Changed)
	require.Equal(t, applied.DocumentRevision, deleted.DocumentRevision)
	require.Nil(t, deleted.TargetRevision)
	var count int64
	require.NoError(t, db.Table("series_translation").Where("entity_id = ? AND locale = 'ko'", seriesID).Count(&count).Error)
	require.Zero(t, count)

	createRequest := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPostSeries,
		Document: identity.Reference, Locale: "ko", ExpectedDocumentRevision: deleted.DocumentRevision,
		Operations: []core.Operation{core.CreateTranslationOperation()},
	}
	created, err := application.Apply(t.Context(), createRequest)
	require.NoError(t, err)
	require.True(t, created.Changed)
	require.Equal(t, deleted.DocumentRevision, created.DocumentRevision)
	require.NotNil(t, created.TargetRevision)
	require.NoError(t, db.Table("series_translation").Where("entity_id = ? AND locale = 'ko'", seriesID).Take(&target).Error)
	require.Nil(t, target.Title)
	require.Nil(t, target.Summary)
}

func TestPostSeriesAIDocumentTargetMutationIsLocaleExactAndSourceInvalidatesEveryTarget(t *testing.T) {
	port, db, seriesID, _, _ := newSeriesAIDocumentFixture(t)
	application, err := core.NewService(port)
	require.NoError(t, err)
	identity := core.DocumentIdentity{Domain: core.DomainPostSeries, Reference: core.DocumentReference(seriesID)}

	now := time.Date(2026, 8, 23, 5, 0, 0, 0, time.UTC)
	koTitle, jaTitle := "한국어", "日本語"
	for _, translation := range []model.SeriesTranslation{
		{EntityID: seriesID, Locale: "ko", Title: &koTitle, CreatedAt: now, UpdatedAt: now},
		{EntityID: seriesID, Locale: "ja", Title: &jaTitle, CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)},
	} {
		require.NoError(t, db.Table("series_translation").Create(&translation).Error)
	}

	sourceBefore, err := port.Load(t.Context(), identity, "en")
	require.NoError(t, err)
	koBefore, err := port.Load(t.Context(), identity, "ko")
	require.NoError(t, err)
	jaBefore, err := port.Load(t.Context(), identity, "ja")
	require.NoError(t, err)
	require.Equal(t, sourceBefore.DocumentRevision, koBefore.DocumentRevision)
	require.Equal(t, sourceBefore.DocumentRevision, jaBefore.DocumentRevision)
	require.NotNil(t, koBefore.TargetRevision)
	require.NotNil(t, jaBefore.TargetRevision)

	koUpdated, err := application.Apply(t.Context(), core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPostSeries,
		Document: identity.Reference, Locale: "ko", ExpectedDocumentRevision: koBefore.DocumentRevision,
		ExpectedTargetRevision: koBefore.TargetRevision,
		Operations: []core.Operation{
			core.SetFieldOperation(postSeriesAIDocumentBlock, postSeriesAIFieldTitle, core.Text("새 한국어")),
		},
	})
	require.NoError(t, err)
	require.Equal(t, koBefore.DocumentRevision, koUpdated.DocumentRevision)
	require.NotEqual(t, *koBefore.TargetRevision, *koUpdated.TargetRevision)
	jaAfterTargetWrite, err := port.Load(t.Context(), identity, "ja")
	require.NoError(t, err)
	require.Equal(t, jaBefore.DocumentRevision, jaAfterTargetWrite.DocumentRevision)
	require.Equal(t, *jaBefore.TargetRevision, *jaAfterTargetWrite.TargetRevision)

	var targetTimesBefore []time.Time
	require.NoError(t, db.Table("series_translation").Where("entity_id = ? AND locale IN ?", seriesID, []string{"ko", "ja"}).
		Order("locale").Pluck("updated_at", &targetTimesBefore).Error)
	sourceUpdated, err := application.Apply(t.Context(), core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPostSeries,
		Document: identity.Reference, Locale: "en", ExpectedDocumentRevision: sourceBefore.DocumentRevision,
		Operations: []core.Operation{
			core.SetFieldOperation(postSeriesAIDocumentBlock, postSeriesAIFieldTitle, core.Text("Updated source")),
		},
	})
	require.NoError(t, err)
	require.NotEqual(t, sourceBefore.DocumentRevision, sourceUpdated.DocumentRevision)
	koAfterSourceWrite, err := port.Load(t.Context(), identity, "ko")
	require.NoError(t, err)
	jaAfterSourceWrite, err := port.Load(t.Context(), identity, "ja")
	require.NoError(t, err)
	require.Equal(t, sourceUpdated.DocumentRevision, koAfterSourceWrite.DocumentRevision)
	require.Equal(t, sourceUpdated.DocumentRevision, jaAfterSourceWrite.DocumentRevision)
	require.NotEqual(t, *koUpdated.TargetRevision, *koAfterSourceWrite.TargetRevision)
	require.NotEqual(t, *jaAfterTargetWrite.TargetRevision, *jaAfterSourceWrite.TargetRevision)
	var targetTimesAfter []time.Time
	require.NoError(t, db.Table("series_translation").Where("entity_id = ? AND locale IN ?", seriesID, []string{"ko", "ja"}).
		Order("locale").Pluck("updated_at", &targetTimesAfter).Error)
	require.Equal(t, targetTimesBefore, targetTimesAfter, "source mutation must invalidate target tokens without rewriting target rows")
}

func TestPostSeriesAIDocumentSourceConstraintsAndStructuralOrder(t *testing.T) {
	port, db, seriesID, postIDs, og := newSeriesAIDocumentFixture(t)
	application, err := core.NewService(port)
	require.NoError(t, err)
	identity := core.DocumentIdentity{Domain: core.DomainPostSeries, Reference: core.DocumentReference(seriesID)}
	source, err := port.Load(t.Context(), identity, "en")
	require.NoError(t, err)

	invalid := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPostSeries,
		Document: identity.Reference, Locale: "en", ExpectedDocumentRevision: source.DocumentRevision,
		Operations: []core.Operation{core.SetFieldOperation(postSeriesAIDocumentBlock, postSeriesAIFieldTitle, core.Text("   "))},
	}
	validation, err := application.Validate(t.Context(), invalid)
	require.NoError(t, err)
	require.Len(t, validation.Issues, 1)
	require.Equal(t, core.IssueValueKindMismatch, validation.Issues[0].Code)

	move := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPostSeries,
		Document: identity.Reference, Locale: "en", ExpectedDocumentRevision: source.DocumentRevision,
		Operations: []core.Operation{core.MoveRelationItemOperation(
			postSeriesAIDocumentBlock, postSeriesAIDocumentPosts, core.RelationItemID(postIDs[1]),
			postSeriesAIDocumentBlock, postSeriesAIDocumentPosts, "",
		)},
	}
	moved, err := application.Apply(t.Context(), move)
	require.NoError(t, err)
	require.True(t, moved.Changed)
	var ordered []string
	require.NoError(t, db.Table("post").Where("series_id = ?", seriesID).Order("series_order ASC").Pluck("id", &ordered).Error)
	require.Equal(t, []string{postIDs[1], postIDs[0]}, ordered)

	title := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPostSeries,
		Document: identity.Reference, Locale: "en", ExpectedDocumentRevision: moved.DocumentRevision,
		Operations: []core.Operation{core.SetFieldOperation(postSeriesAIDocumentBlock, postSeriesAIFieldTitle, core.Text("Next title"))},
	}
	updated, err := application.Apply(t.Context(), title)
	require.NoError(t, err)
	require.True(t, updated.Changed)
	require.Equal(t, []string{"en"}, og.locales)

}

func TestPostSeriesAIDocumentSelectsOneExactActionPerCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		operations []core.Operation
		want       seriesAction
	}{
		{name: "copy", operations: []core.Operation{core.SetFieldOperation(postSeriesAIDocumentBlock, postSeriesAIFieldTitle, core.Text("title"))}, want: policyv1.PostSeries.Edit},
		{name: "translation", operations: []core.Operation{core.CreateTranslationOperation()}, want: policyv1.PostSeries.Edit},
		{name: "publish", operations: []core.Operation{core.SetFieldOperation(postSeriesAIDocumentBlock, postSeriesAIFieldStatus, core.Text("SERIES_STATUS_PUBLISHED"))}, want: policyv1.PostSeries.Publish},
		{name: "structure wins", operations: []core.Operation{
			core.SetFieldOperation(postSeriesAIDocumentBlock, postSeriesAIFieldStatus, core.Text("SERIES_STATUS_PUBLISHED")),
			core.MoveRelationItemOperation(postSeriesAIDocumentBlock, postSeriesAIDocumentPosts, "post-1", postSeriesAIDocumentBlock, postSeriesAIDocumentPosts, ""),
		}, want: policyv1.PostSeries.Manage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			const seriesID = "series-action-test"
			got, err := postSeriesAIDocumentAction(test.operations)(seriesID)
			require.NoError(t, err)
			want, err := test.want(seriesID)
			require.NoError(t, err)
			require.Equal(t, want.EngineKey(), got.EngineKey())
		})
	}
}

func TestPostSeriesExactMutationUsesOneDecisionAndValidateRollsBack(t *testing.T) {
	port, db, seriesID, _, _ := newSeriesAIDocumentFixture(t)
	access := &recordingSeriesAIDocumentAccess{}
	port.access = access
	application, err := core.NewService(port)
	require.NoError(t, err)
	identity := core.DocumentIdentity{
		Domain: core.DomainPostSeries, Reference: core.DocumentReference(seriesID),
	}
	loaded, err := port.Load(t.Context(), identity, "en")
	require.NoError(t, err)
	access.actions = nil
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPostSeries,
		Document: identity.Reference, Locale: "en", ExpectedDocumentRevision: loaded.DocumentRevision,
		Operations: []core.Operation{
			core.SetFieldOperation(postSeriesAIDocumentBlock, postSeriesAIFieldTitle, core.Text("Validated title")),
		},
	}

	validation, err := application.Validate(t.Context(), request)
	require.NoError(t, err)
	require.True(t, validation.Valid())
	var title *string
	require.NoError(t, db.Table("series_translation").Select("title").
		Where("entity_id = ? AND locale = 'en'", seriesID).Scan(&title).Error)
	require.NotNil(t, title)
	require.Equal(t, "Source title", *title, "Validate must roll the owning transaction back")

	result, err := application.Apply(t.Context(), request)
	require.NoError(t, err)
	require.True(t, result.Changed)
	wantAction, err := policyv1.PostSeries.Edit(seriesID)
	require.NoError(t, err)
	require.Len(t, access.actions, 2)
	for _, action := range access.actions {
		require.Equal(t, wantAction.EngineKey(), action.EngineKey(), "Validate and Apply must each make one exact decision")
	}
	require.NoError(t, db.Table("series_translation").Select("title").
		Where("entity_id = ? AND locale = 'en'", seriesID).Scan(&title).Error)
	require.NotNil(t, title)
	require.Equal(t, "Validated title", *title)
}

func newSeriesAIDocumentFixture(t *testing.T) (*AIDocumentService, *gorm.DB, string, []string, *seriesAIDocumentOG) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	statements := []string{
		`CREATE TABLE content_document (id TEXT PRIMARY KEY, profile TEXT NOT NULL, revision TEXT NOT NULL, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`,
		`CREATE TABLE series (id TEXT PRIMARY KEY, slug TEXT NOT NULL, status TEXT NOT NULL, source_locale TEXT NOT NULL, content_document_id TEXT NOT NULL, featured_image_file_id TEXT, created_at DATETIME NOT NULL, updated_at DATETIME)`,
		`CREATE TABLE series_translation (entity_id TEXT NOT NULL, locale TEXT NOT NULL, title TEXT, summary TEXT, content_json BLOB, content_html TEXT, content_text TEXT, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL, og_asset_id TEXT, PRIMARY KEY (entity_id, locale))`,
		`CREATE TABLE post (id TEXT PRIMARY KEY, series_id TEXT, series_order INTEGER)`,
	}
	for _, statement := range statements {
		require.NoError(t, db.Exec(statement).Error)
	}
	now := time.Date(2026, 8, 23, 4, 0, 0, 0, time.UTC)
	seriesID := uuid.NewString()
	contentDocumentID := uuid.NewString()
	contentDocumentRevision := uuid.NewString()
	require.NoError(t, db.Exec("INSERT INTO content_document (id, profile, revision, created_at, updated_at) VALUES (?, ?, ?, ?, ?)", contentDocumentID, "compact", contentDocumentRevision, now, now).Error)
	require.NoError(t, db.Create(&model.Series{
		ID: seriesID, Slug: "post-series", Status: "SERIES_STATUS_DRAFT", SourceLocale: "en",
		ContentDocumentID: contentDocumentID,
		CreatedAt:         now, UpdatedAt: &now,
	}).Error)
	title := "Source title"
	summary := "Source summary"
	require.NoError(t, db.Table("series_translation").Create(&model.SeriesTranslation{
		EntityID: seriesID, Locale: "en", Title: &title, Summary: &summary,
		ContentText: &summary, CreatedAt: now, UpdatedAt: now,
	}).Error)
	postIDs := []string{uuid.NewString(), uuid.NewString()}
	for index, postID := range postIDs {
		require.NoError(t, db.Exec("INSERT INTO post (id, series_id, series_order) VALUES (?, ?, ?)", postID, seriesID, index).Error)
	}
	og := &seriesAIDocumentOG{}
	service := &AIDocumentService{
		db: db, spiceDB: &auth.SpiceDBClient{}, access: allowSeriesAIDocumentAccess{},
		menuTargets: seriesAIDocumentMenu{},
		postAccess:  seriesAIDocumentPostAccess{}, ogRefresh: og, now: func() time.Time { return now.Add(time.Minute) },
	}
	return service, db, seriesID, postIDs, og
}

func requireAIDocumentLocalizedText(t *testing.T, document core.Document, field core.FieldID) string {
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
