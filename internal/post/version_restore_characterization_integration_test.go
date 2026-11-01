//go:build integration

package post_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil/contenttest"

	"github.com/echovisionlab/geul-api/internal/adapters/filemediareuse"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	postruntime "github.com/echovisionlab/geul-api/internal/adapters/post/runtime"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mq"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// The version restore characterization tests for Post, Page and Work share
// one case table shape so the three restore paths can later share a helper:
// a checkpoint is taken from a known source state, the entity drifts, and the
// restore must return it to the checkpoint while emitting exactly the pinned
// side effects.

type postVersionRestoreEntity struct {
	id         string
	documentID string
	revision   string
	blockID    string
}

type postVersionRestoreResponse struct {
	revision     string
	title        string
	summary      *string
	sourceLocale string
}

type postVersionRestoreWant struct {
	unchanged bool
	// eventFields are the content.updated changed fields as "path KIND".
	eventFields          []string
	documentStateChanged bool
	// eventLocale is empty when the event carries no locale tuple.
	eventLocale   string
	ogGenerations int64
}

type postVersionRestoreCase struct {
	name  string
	drift func(t *testing.T, f *postVersionRestoreFixture, entity *postVersionRestoreEntity)
	want  postVersionRestoreWant
}

const (
	postVersionRestoreTitle   = "Version restore source"
	postVersionRestoreBody    = "Checkpoint body"
	postVersionRestoreOGEvent = "post_version_restored"
)

func TestPostVersionRestoreCharacterizationIntegration(t *testing.T) {
	t.Parallel()
	f := newPostVersionRestoreFixture(t)
	changedSummary := "Drifted summary"
	cases := []postVersionRestoreCase{
		{
			name: "blocks only",
			drift: func(t *testing.T, f *postVersionRestoreFixture, entity *postVersionRestoreEntity) {
				f.applyBody(t, entity, "Drifted body")
			},
			want: postVersionRestoreWant{
				eventFields:          []string{"document.content TEXT"},
				documentStateChanged: true, eventLocale: "en",
			},
		},
		{
			name: "metadata only",
			drift: func(t *testing.T, f *postVersionRestoreFixture, entity *postVersionRestoreEntity) {
				f.updateMetadata(t, entity, "Drifted title", &changedSummary)
			},
			want: postVersionRestoreWant{
				eventFields:          []string{"title TEXT", "summary TEXT"},
				documentStateChanged: true, eventLocale: "en", ogGenerations: 1,
			},
		},
		{
			name: "summary only",
			drift: func(t *testing.T, f *postVersionRestoreFixture, entity *postVersionRestoreEntity) {
				f.updateMetadata(t, entity, postVersionRestoreTitle, &changedSummary)
			},
			want: postVersionRestoreWant{
				eventFields:          []string{"summary TEXT"},
				documentStateChanged: true, eventLocale: "en",
			},
		},
		{
			name: "blocks and metadata",
			drift: func(t *testing.T, f *postVersionRestoreFixture, entity *postVersionRestoreEntity) {
				f.updateMetadata(t, entity, "Drifted title", &changedSummary)
				f.applyBody(t, entity, "Drifted body")
			},
			want: postVersionRestoreWant{
				eventFields:          []string{"title TEXT", "summary TEXT", "document.content TEXT"},
				documentStateChanged: true, eventLocale: "en", ogGenerations: 1,
			},
		},
		{
			name: "source locale switch",
			drift: func(t *testing.T, f *postVersionRestoreFixture, entity *postVersionRestoreEntity) {
				f.switchSourceLocale(t, entity, "ko", "Korean source title")
			},
			want: postVersionRestoreWant{
				eventFields:          []string{"settings.source_locale CONFIGURATION"},
				documentStateChanged: true, eventLocale: "en", ogGenerations: 1,
			},
		},
		{
			name:  "unchanged",
			drift: func(*testing.T, *postVersionRestoreFixture, *postVersionRestoreEntity) {},
			want:  postVersionRestoreWant{unchanged: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entity := f.create(t)
			f.applyBody(t, entity, postVersionRestoreBody)
			versionID := f.checkpoint(t, entity)
			checkpointRows := contenttest.LoadContentDocumentRows(t, f.db, entity.documentID)
			checkpointSettings := f.settingsFingerprint(t, entity)
			tc.drift(t, f, entity)

			beforeRows := contenttest.LoadContentDocumentRows(t, f.db, entity.documentID)
			beforeMetadata := contenttest.LoadLocaleMetadataRows(t, f.db, "post_translation", entity.id)
			beforeUpdatedAt := f.updatedAt(t, entity)
			beforeEvents := len(contenttest.ContentUpdatedEvents(t, f.db, entity.id))
			beforeAudit := len(contenttest.LoadDomainAuditRows(t, f.db, "post", entity.id))

			restored, err := f.restore(f.ctx, entity.id, versionID)
			require.NoError(t, err)

			afterRows := contenttest.LoadContentDocumentRows(t, f.db, entity.documentID)
			afterMetadata := contenttest.LoadLocaleMetadataRows(t, f.db, "post_translation", entity.id)
			events := contenttest.ContentUpdatedEvents(t, f.db, entity.id)[beforeEvents:]
			audit := contenttest.LoadDomainAuditRows(t, f.db, "post", entity.id)[beforeAudit:]

			require.Equal(t, postVersionRestoreTitle, restored.title)
			require.Nil(t, restored.summary)
			require.Equal(t, "en", restored.sourceLocale)
			require.Equal(t, "en", f.sourceLocale(t, entity))
			require.Equal(t, afterRows.Revision, restored.revision)
			require.Equal(t, checkpointRows.Blocks, afterRows.Blocks)
			require.Equal(t, postVersionRestoreLocales(checkpointRows, "en", true), postVersionRestoreLocales(afterRows, "en", true))
			require.Equal(t, postVersionRestoreLocales(beforeRows, "en", false), postVersionRestoreLocales(afterRows, "en", false),
				"target locale overlays stay unchanged")
			require.Equal(t, checkpointSettings, f.settingsFingerprint(t, entity), "versions never restore settings")
			require.Equal(t, tc.want.ogGenerations, contenttest.CountOgGenerationsForReason(t, f.db, entity.id, postVersionRestoreOGEvent))

			if tc.want.unchanged {
				require.Equal(t, beforeRows, afterRows)
				require.Equal(t, beforeMetadata, afterMetadata)
				require.Equal(t, beforeUpdatedAt, f.updatedAt(t, entity))
				require.Empty(t, events)
				require.Empty(t, audit)
				return
			}
			require.NotEqual(t, beforeRows.Revision, afterRows.Revision)
			require.True(t, f.updatedAt(t, entity).After(beforeUpdatedAt))
			source := requirePostVersionRestoreLocaleRow(t, afterMetadata, "en")
			require.Equal(t, postVersionRestoreTitle, *source.Title)
			require.Nil(t, source.Summary)
			for _, row := range beforeMetadata {
				if row.Locale != "en" {
					require.Equal(t, row, requirePostVersionRestoreLocaleRow(t, afterMetadata, row.Locale), "target locale rows stay unchanged")
				}
			}
			require.Equal(t, []contenttest.DomainAuditRow{{
				Action:        string(sharedtelemetry.AuditPostUpdated),
				ActorKind:     string(sharedtelemetry.ActorKindMember),
				ActorMemberID: &f.memberID,
				ChangedFields: []string{"version_restore"},
				VersionID:     &versionID,
			}}, audit)
			require.Len(t, events, 1)
			requirePostVersionRestoreEvent(t, events[0], entity.id, f.memberID, afterRows.Revision, tc.want)

			err = f.applyBodyAt(t, entity, beforeRows.Revision, "Stale writer body")
			require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "a writer holding the pre-restore revision conflicts")
		})
	}
}

func TestPostVersionRestoreRejectionsIntegration(t *testing.T) {
	t.Parallel()
	f := newPostVersionRestoreFixture(t)
	outsiderID := orytest.IntegrationUUID()
	orytest.SeedPostIntegrationIdentity(t, f.db, outsiderID, "Post version restore outsider")
	outsiderCtx := withPostAuditedRequestContext(t, orytest.PostIntegrationContext(outsiderID))
	missingVersionID := uuid.NewString()
	cases := []struct {
		name        string
		ctx         context.Context
		request     func(t *testing.T, entity, other *postVersionRestoreEntity, versionID string) (string, string)
		wantCode    connect.Code
		wantMessage func(entity *postVersionRestoreEntity, versionID string) string
	}{
		{
			name: "unknown version",
			ctx:  f.ctx,
			request: func(_ *testing.T, entity, _ *postVersionRestoreEntity, _ string) (string, string) {
				return entity.id, missingVersionID
			},
			wantCode:    connect.CodeNotFound,
			wantMessage: func(*postVersionRestoreEntity, string) string { return "post_version not found: " + missingVersionID },
		},
		{
			name: "version of another post",
			ctx:  f.ctx,
			request: func(_ *testing.T, _, other *postVersionRestoreEntity, versionID string) (string, string) {
				return other.id, versionID
			},
			wantCode: connect.CodeNotFound,
			wantMessage: func(_ *postVersionRestoreEntity, versionID string) string {
				return "post_version not found: " + versionID
			},
		},
		{
			name: "invalid snapshot",
			ctx:  f.ctx,
			request: func(t *testing.T, entity, _ *postVersionRestoreEntity, versionID string) (string, string) {
				f.corruptSnapshotDocument(t, versionID)
				return entity.id, versionID
			},
			wantCode:    connect.CodeFailedPrecondition,
			wantMessage: func(*postVersionRestoreEntity, string) string { return "Post Version typed snapshot is invalid" },
		},
		{
			name: "without manage permission",
			ctx:  outsiderCtx,
			request: func(_ *testing.T, entity, _ *postVersionRestoreEntity, versionID string) (string, string) {
				return entity.id, versionID
			},
			wantCode:    connect.CodePermissionDenied,
			wantMessage: func(*postVersionRestoreEntity, string) string { return "no permission to manage this post" },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entity := f.create(t)
			f.applyBody(t, entity, postVersionRestoreBody)
			versionID := f.checkpoint(t, entity)
			f.applyBody(t, entity, "Drifted body")
			other := f.create(t)
			beforeRows := contenttest.LoadContentDocumentRows(t, f.db, entity.documentID)
			beforeEvents := len(contenttest.ContentUpdatedEvents(t, f.db, entity.id))
			beforeAudit := len(contenttest.LoadDomainAuditRows(t, f.db, "post", entity.id))

			entityID, requestVersionID := tc.request(t, entity, other, versionID)
			_, err := f.restore(tc.ctx, entityID, requestVersionID)
			require.Equal(t, tc.wantCode, connect.CodeOf(err), "%v", err)
			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			require.Equal(t, tc.wantMessage(entity, versionID), connectErr.Message())

			require.Equal(t, beforeRows, contenttest.LoadContentDocumentRows(t, f.db, entity.documentID))
			require.Len(t, contenttest.ContentUpdatedEvents(t, f.db, entity.id), beforeEvents)
			require.Len(t, contenttest.LoadDomainAuditRows(t, f.db, "post", entity.id), beforeAudit)
		})
	}
}

func requirePostVersionRestoreEvent(
	t *testing.T,
	event *managev1.ContentUpdatedEvent,
	entityID string,
	memberID string,
	revision string,
	want postVersionRestoreWant,
) {
	t.Helper()
	require.Equal(t, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST, event.GetEntityType())
	require.Equal(t, entityID, event.GetEntityId())
	require.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE, event.GetSource())
	fields := make([]string, 0, len(event.GetChangedFields()))
	for _, field := range event.GetChangedFields() {
		fields = append(fields, field.GetPath()+" "+strings.TrimPrefix(field.GetKind().String(), "CONTENT_UPDATED_FIELD_KIND_"))
	}
	require.Equal(t, want.eventFields, fields)
	require.Equal(t, want.documentStateChanged, event.GetDocumentStateChanged())
	require.Equal(t, revision, event.GetDocumentRevision())
	require.Equal(t, []string{memberID}, event.GetContributorMemberIds())
	require.Nil(t, event.TargetRevision)
	if want.eventLocale == "" {
		require.Nil(t, event.Locale)
		require.Nil(t, event.LocaleExists)
		return
	}
	require.Equal(t, want.eventLocale, event.GetLocale())
	require.True(t, event.GetLocaleExists())
}

// postVersionRestoreLocales keeps the block-locale rows of locale, or every
// other locale when matching is false.
func postVersionRestoreLocales(rows contenttest.ContentDocumentRows, locale string, matching bool) []contenttest.ContentBlockLocaleRow {
	var selected []contenttest.ContentBlockLocaleRow
	for _, row := range rows.Locales {
		if (row.Locale == locale) == matching {
			selected = append(selected, row)
		}
	}
	return selected
}

func requirePostVersionRestoreLocaleRow(t *testing.T, rows []contenttest.LocaleMetadataRow, locale string) contenttest.LocaleMetadataRow {
	t.Helper()
	for _, row := range rows {
		if row.Locale == locale {
			return row
		}
	}
	t.Fatalf("locale %s row is missing", locale)
	return contenttest.LocaleMetadataRow{}
}

type postVersionRestoreFixture struct {
	db        *gorm.DB
	ctx       context.Context
	systemCtx context.Context
	memberID  string
	manage    *postdomain.ConnectHandler
	internal  *postdomain.InternalConnectHandler
}

func newPostVersionRestoreFixture(t *testing.T) *postVersionRestoreFixture {
	t.Helper()
	db := orytest.NewIntegrationDB(t)
	adminID := orytest.IntegrationUUID()
	orytest.SeedPostIntegrationIdentity(t, db, adminID, "Post version restore admin")
	spiceDB := orytest.PostIntegrationSpiceDB(t)
	orytest.GrantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	sqlDB, err := db.DB()
	require.NoError(t, err)
	publisher, err := mq.NewPublisher(sqlDB)
	require.NoError(t, err)
	writer := apitelemetry.NewDurableWriter(db)
	contentBlocks, err := contentblock.NewGeneratedStore(filemediareuse.New(spiceDB))
	require.NoError(t, err)
	service := postintegration.NewService(t, postintegration.Deps{
		DB: db, SpiceDB: spiceDB, Files: postAuditFiles{}, Signals: publisher,
		MediaLoader:    postruntime.ContentBlockMedia{},
		VersionRestore: postruntime.VersionRestore{}, ContentBlocks: contentBlocks, Audit: writer,
	})
	return &postVersionRestoreFixture{
		db:        db,
		ctx:       withPostAuditedRequestContext(t, orytest.PostIntegrationContext(adminID)),
		systemCtx: postAuditedSystemRequestContext(t),
		memberID:  orytest.PostIntegrationMemberID(adminID),
		manage:    postdomain.NewConnectHandler(service),
		internal:  postdomain.NewInternalConnectHandler(service),
	}
}

func (f *postVersionRestoreFixture) create(t *testing.T) *postVersionRestoreEntity {
	t.Helper()
	created, err := f.manage.CreatePost(f.ctx, connect.NewRequest(&managev1.CreatePostRequest{
		Title: postVersionRestoreTitle, Document: contenttest.EmptyPostDocument("en"),
	}))
	require.NoError(t, err)
	var documentID string
	require.NoError(t, f.db.Table("post").Select("content_document_id::text").
		Where("id = ?", created.Msg.Id).Scan(&documentID).Error)
	return &postVersionRestoreEntity{
		id: created.Msg.Id, documentID: documentID, revision: created.Msg.Revision, blockID: uuid.NewString(),
	}
}

func (f *postVersionRestoreFixture) applyBody(t *testing.T, entity *postVersionRestoreEntity, text string) {
	t.Helper()
	require.NoError(t, f.applyBodyAt(t, entity, entity.revision, text))
}

func (f *postVersionRestoreFixture) applyBodyAt(t *testing.T, entity *postVersionRestoreEntity, revision, text string) error {
	t.Helper()
	applied, err := f.internal.ApplyPostBlockBatch(f.ctx, connect.NewRequest(&intrav1.ApplyPostBlockBatchRequest{
		PostId: entity.id, Locale: "en",
		Batch: contenttest.NewParagraphBlockBatch(
			contenttest.EmptyPostDocument("en"), revision, "en", entity.blockID, text, []string{f.memberID},
		),
	}))
	if err != nil {
		return err
	}
	entity.revision = applied.Msg.DocumentRevision
	return nil
}

func (f *postVersionRestoreFixture) updateMetadata(t *testing.T, entity *postVersionRestoreEntity, title string, summary *string) {
	t.Helper()
	request := &intrav1.UpdatePostLocaleMetadataRequest{
		PostId: entity.id, Locale: "en", ExpectedRevision: entity.revision,
		ContributorMemberIds: []string{f.memberID},
		TitleChange:          &intrav1.PostNullableStringChange{Value: &intrav1.PostNullableStringChange_SetValue{SetValue: title}},
	}
	if summary != nil {
		request.SummaryChange = &intrav1.PostNullableStringChange{Value: &intrav1.PostNullableStringChange_SetValue{SetValue: *summary}}
	}
	updated, err := f.internal.UpdatePostLocaleMetadata(f.ctx, connect.NewRequest(request))
	require.NoError(t, err)
	entity.revision = updated.Msg.DocumentRevision
}

func (f *postVersionRestoreFixture) checkpoint(t *testing.T, entity *postVersionRestoreEntity) string {
	t.Helper()
	checkpoint, err := f.internal.CreatePostVersionCheckpoint(f.systemCtx, connect.NewRequest(&intrav1.CreatePostVersionCheckpointRequest{
		PostId: entity.id, ExpectedRevision: entity.revision, ContributorMemberIds: []string{f.memberID}, Locale: "en",
	}))
	require.NoError(t, err)
	require.True(t, checkpoint.Msg.Created)
	return checkpoint.Msg.GetVersionId()
}

// switchSourceLocale moves source authority with SQL so the drift does not
// depend on a source-promotion API. The new source locale receives a copy of
// every source overlay, as a promoted translation would have.
func (f *postVersionRestoreFixture) switchSourceLocale(t *testing.T, entity *postVersionRestoreEntity, locale, title string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, f.db.Exec(`
		INSERT INTO content_block_locale (block_id, locale, localized_data)
		SELECT l.block_id, ?, l.localized_data FROM content_block_locale l
		JOIN content_block b ON b.id = l.block_id
		WHERE b.document_id = ?::uuid AND l.locale = 'en'
	`, locale, entity.documentID).Error)
	require.NoError(t, f.db.Exec(`
		INSERT INTO post_translation (entity_id, locale, title, created_at, updated_at)
		VALUES (?::uuid, ?, ?, ?, ?)
	`, entity.id, locale, title, now, now).Error)
	require.NoError(t, f.db.Table("post").Where("id = ?::uuid", entity.id).Update("source_locale", locale).Error)
}

func (f *postVersionRestoreFixture) corruptSnapshotDocument(t *testing.T, versionID string) {
	t.Helper()
	require.NoError(t, f.db.Exec(`
		UPDATE post_version SET content_snapshot = jsonb_set(content_snapshot, '{document}', '{"unknownField": true}')
		WHERE id = ?::uuid
	`, versionID).Error)
}

func (f *postVersionRestoreFixture) restore(ctx context.Context, entityID, versionID string) (postVersionRestoreResponse, error) {
	restored, err := f.manage.RestorePostVersion(ctx, connect.NewRequest(&managev1.RestorePostVersionRequest{
		PostId: entityID, VersionId: versionID,
	}))
	if err != nil {
		return postVersionRestoreResponse{}, err
	}
	return postVersionRestoreResponse{
		revision: restored.Msg.GetRevision(), title: restored.Msg.GetTitle(),
		summary: restored.Msg.Summary, sourceLocale: restored.Msg.GetSourceLocale(),
	}, nil
}

func (f *postVersionRestoreFixture) sourceLocale(t *testing.T, entity *postVersionRestoreEntity) string {
	t.Helper()
	var locale string
	require.NoError(t, f.db.Table("post").Select("source_locale").Where("id = ?", entity.id).Scan(&locale).Error)
	return locale
}

func (f *postVersionRestoreFixture) updatedAt(t *testing.T, entity *postVersionRestoreEntity) time.Time {
	t.Helper()
	var updatedAt time.Time
	require.NoError(t, f.db.Table("post").Select("updated_at").Where("id = ?", entity.id).Scan(&updatedAt).Error)
	return updatedAt.UTC().Truncate(time.Microsecond)
}

// settingsFingerprint is the configuration revision: restore leaves every
// Post setting current.
func (f *postVersionRestoreFixture) settingsFingerprint(t *testing.T, entity *postVersionRestoreEntity) string {
	t.Helper()
	var revision string
	require.NoError(t, f.db.Table("post").Select("configuration_revision::text").Where("id = ?", entity.id).Scan(&revision).Error)
	require.NotEmpty(t, revision)
	return revision
}
