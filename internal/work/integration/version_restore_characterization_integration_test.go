//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mq"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
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

type workVersionRestoreEntity struct {
	id         string
	documentID string
	revision   string
	blockID    string
}

type workVersionRestoreResponse struct {
	revision     string
	title        string
	summary      *string
	sourceLocale string
}

type workVersionRestoreWant struct {
	unchanged bool
	// eventFields are the content.updated changed fields as "path KIND".
	eventFields          []string
	documentStateChanged bool
	// eventLocale is empty when the event carries no locale tuple.
	eventLocale   string
	ogGenerations int64
}

type workVersionRestoreCase struct {
	name  string
	drift func(t *testing.T, f *workVersionRestoreFixture, entity *workVersionRestoreEntity)
	want  workVersionRestoreWant
}

const (
	workVersionRestoreTitle   = "Version restore source"
	workVersionRestoreBody    = "Checkpoint body"
	workVersionRestoreOGEvent = "work_version_restored"
)

func TestWorkVersionRestoreCharacterizationIntegration(t *testing.T) {
	t.Parallel()
	f := newWorkVersionRestoreFixture(t)
	changedSummary := "Drifted summary"
	cases := []workVersionRestoreCase{
		{
			name: "blocks only",
			drift: func(t *testing.T, f *workVersionRestoreFixture, entity *workVersionRestoreEntity) {
				f.applyBody(t, entity, "Drifted body")
			},
			want: workVersionRestoreWant{
				eventFields:          []string{"document.content TEXT"},
				documentStateChanged: true, eventLocale: "en", ogGenerations: 1,
			},
		},
		{
			name: "metadata only",
			drift: func(t *testing.T, f *workVersionRestoreFixture, entity *workVersionRestoreEntity) {
				f.updateMetadata(t, entity, "Drifted title", &changedSummary)
			},
			want: workVersionRestoreWant{
				eventFields:          []string{"title TEXT", "summary TEXT"},
				documentStateChanged: true, eventLocale: "en", ogGenerations: 1,
			},
		},
		{
			name: "summary only",
			drift: func(t *testing.T, f *workVersionRestoreFixture, entity *workVersionRestoreEntity) {
				f.updateMetadata(t, entity, workVersionRestoreTitle, &changedSummary)
			},
			want: workVersionRestoreWant{
				eventFields:          []string{"summary TEXT"},
				documentStateChanged: true, eventLocale: "en", ogGenerations: 1,
			},
		},
		{
			name: "blocks and metadata",
			drift: func(t *testing.T, f *workVersionRestoreFixture, entity *workVersionRestoreEntity) {
				f.updateMetadata(t, entity, "Drifted title", &changedSummary)
				f.applyBody(t, entity, "Drifted body")
			},
			want: workVersionRestoreWant{
				eventFields:          []string{"title TEXT", "summary TEXT", "document.content TEXT"},
				documentStateChanged: true, eventLocale: "en", ogGenerations: 1,
			},
		},
		{
			name: "source locale switch",
			drift: func(t *testing.T, f *workVersionRestoreFixture, entity *workVersionRestoreEntity) {
				f.switchSourceLocale(t, entity, "ko", "Korean source title")
			},
			want: workVersionRestoreWant{
				eventFields:          []string{"settings.source_locale CONFIGURATION"},
				documentStateChanged: true, eventLocale: "en", ogGenerations: 1,
			},
		},
		{
			name:  "unchanged",
			drift: func(*testing.T, *workVersionRestoreFixture, *workVersionRestoreEntity) {},
			want:  workVersionRestoreWant{unchanged: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entity := f.create(t)
			f.applyBody(t, entity, workVersionRestoreBody)
			versionID := f.checkpoint(t, entity)
			checkpointRows := testutil.LoadContentDocumentRows(t, f.db, entity.documentID)
			checkpointSettings := f.settingsFingerprint(t, entity)
			tc.drift(t, f, entity)

			beforeRows := testutil.LoadContentDocumentRows(t, f.db, entity.documentID)
			beforeMetadata := testutil.LoadLocaleMetadataRows(t, f.db, "work_translation", entity.id)
			beforeUpdatedAt := f.updatedAt(t, entity)
			beforeEvents := len(testutil.ContentUpdatedEvents(t, f.db, entity.id))
			beforeAudit := len(testutil.LoadDomainAuditRows(t, f.db, "work", entity.id))

			restored, err := f.restore(f.ctx, entity.id, versionID)
			require.NoError(t, err)

			afterRows := testutil.LoadContentDocumentRows(t, f.db, entity.documentID)
			afterMetadata := testutil.LoadLocaleMetadataRows(t, f.db, "work_translation", entity.id)
			events := testutil.ContentUpdatedEvents(t, f.db, entity.id)[beforeEvents:]
			audit := testutil.LoadDomainAuditRows(t, f.db, "work", entity.id)[beforeAudit:]

			require.Equal(t, workVersionRestoreTitle, restored.title)
			require.Nil(t, restored.summary)
			require.Equal(t, "en", restored.sourceLocale)
			require.Equal(t, "en", f.sourceLocale(t, entity))
			require.Equal(t, afterRows.Revision, restored.revision)
			require.Equal(t, checkpointRows.Blocks, afterRows.Blocks)
			require.Equal(t, workVersionRestoreLocales(checkpointRows, "en", true), workVersionRestoreLocales(afterRows, "en", true))
			require.Equal(t, workVersionRestoreLocales(beforeRows, "en", false), workVersionRestoreLocales(afterRows, "en", false),
				"target locale overlays stay unchanged")
			require.Equal(t, checkpointSettings, f.settingsFingerprint(t, entity), "versions never restore settings")
			require.Equal(t, tc.want.ogGenerations, testutil.CountOgGenerationsForReason(t, f.db, entity.id, workVersionRestoreOGEvent))

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
			source := requireWorkVersionRestoreLocaleRow(t, afterMetadata, "en")
			require.Equal(t, workVersionRestoreTitle, *source.Title)
			require.Nil(t, source.Summary)
			for _, row := range beforeMetadata {
				if row.Locale != "en" {
					require.Equal(t, row, requireWorkVersionRestoreLocaleRow(t, afterMetadata, row.Locale), "target locale rows stay unchanged")
				}
			}
			require.Equal(t, []testutil.DomainAuditRow{{
				Action:        string(sharedtelemetry.AuditWorkUpdated),
				ActorKind:     string(sharedtelemetry.ActorKindMember),
				ActorMemberID: &f.memberID,
				ChangedFields: []string{"version_restore"},
				VersionID:     &versionID,
			}}, audit)
			require.Len(t, events, 1)
			requireWorkVersionRestoreEvent(t, events[0], entity.id, f.memberID, afterRows.Revision, tc.want)

			err = f.applyBodyAt(t, entity, beforeRows.Revision, "Stale writer body")
			require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "a writer holding the pre-restore revision conflicts")
		})
	}
}

func TestWorkVersionRestoreRejectionsIntegration(t *testing.T) {
	t.Parallel()
	f := newWorkVersionRestoreFixture(t)
	outsiderID := testutil.IntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, f.db, outsiderID, "Work version restore outsider")
	outsiderCtx := withWorkAuditedRequestContext(t, workIntegrationAdminCtx(outsiderID))
	missingVersionID := uuid.NewString()
	cases := []struct {
		name        string
		ctx         context.Context
		request     func(t *testing.T, entity, other *workVersionRestoreEntity, versionID string) (string, string)
		wantCode    connect.Code
		wantMessage func(entity *workVersionRestoreEntity, versionID string) string
	}{
		{
			name: "unknown version",
			ctx:  f.ctx,
			request: func(_ *testing.T, entity, _ *workVersionRestoreEntity, _ string) (string, string) {
				return entity.id, missingVersionID
			},
			wantCode:    connect.CodeNotFound,
			wantMessage: func(*workVersionRestoreEntity, string) string { return "work_version not found: " + missingVersionID },
		},
		{
			name: "version of another work",
			ctx:  f.ctx,
			request: func(_ *testing.T, _, other *workVersionRestoreEntity, versionID string) (string, string) {
				return other.id, versionID
			},
			wantCode: connect.CodeNotFound,
			wantMessage: func(_ *workVersionRestoreEntity, versionID string) string {
				return "work_version not found: " + versionID
			},
		},
		{
			name: "invalid snapshot",
			ctx:  f.ctx,
			request: func(t *testing.T, entity, _ *workVersionRestoreEntity, versionID string) (string, string) {
				f.corruptSnapshotDocument(t, versionID)
				return entity.id, versionID
			},
			wantCode:    connect.CodeFailedPrecondition,
			wantMessage: func(*workVersionRestoreEntity, string) string { return "Work Version typed snapshot is invalid" },
		},
		{
			name: "snapshot without source title",
			ctx:  f.ctx,
			request: func(t *testing.T, entity, _ *workVersionRestoreEntity, versionID string) (string, string) {
				f.clearSnapshotTitle(t, versionID)
				return entity.id, versionID
			},
			wantCode:    connect.CodeFailedPrecondition,
			wantMessage: func(*workVersionRestoreEntity, string) string { return "Work Version source title is invalid" },
		},
		{
			name: "without manage permission",
			ctx:  outsiderCtx,
			request: func(_ *testing.T, entity, _ *workVersionRestoreEntity, versionID string) (string, string) {
				return entity.id, versionID
			},
			// Unlike Post, a Work restore hides the Work from a Member without edit permission.
			wantCode:    connect.CodeNotFound,
			wantMessage: func(entity *workVersionRestoreEntity, _ string) string { return "work not found: " + entity.id },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entity := f.create(t)
			f.applyBody(t, entity, workVersionRestoreBody)
			versionID := f.checkpoint(t, entity)
			f.applyBody(t, entity, "Drifted body")
			other := f.create(t)
			beforeRows := testutil.LoadContentDocumentRows(t, f.db, entity.documentID)
			beforeEvents := len(testutil.ContentUpdatedEvents(t, f.db, entity.id))
			beforeAudit := len(testutil.LoadDomainAuditRows(t, f.db, "work", entity.id))

			entityID, requestVersionID := tc.request(t, entity, other, versionID)
			_, err := f.restore(tc.ctx, entityID, requestVersionID)
			require.Equal(t, tc.wantCode, connect.CodeOf(err), "%v", err)
			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			require.Equal(t, tc.wantMessage(entity, versionID), connectErr.Message())

			require.Equal(t, beforeRows, testutil.LoadContentDocumentRows(t, f.db, entity.documentID))
			require.Len(t, testutil.ContentUpdatedEvents(t, f.db, entity.id), beforeEvents)
			require.Len(t, testutil.LoadDomainAuditRows(t, f.db, "work", entity.id), beforeAudit)
		})
	}
}

func requireWorkVersionRestoreEvent(
	t *testing.T,
	event *managev1.ContentUpdatedEvent,
	entityID string,
	memberID string,
	revision string,
	want workVersionRestoreWant,
) {
	t.Helper()
	require.Equal(t, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK, event.GetEntityType())
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

// workVersionRestoreLocales keeps the block-locale rows of locale, or every
// other locale when matching is false.
func workVersionRestoreLocales(rows testutil.ContentDocumentRows, locale string, matching bool) []testutil.ContentBlockLocaleRow {
	var selected []testutil.ContentBlockLocaleRow
	for _, row := range rows.Locales {
		if (row.Locale == locale) == matching {
			selected = append(selected, row)
		}
	}
	return selected
}

func requireWorkVersionRestoreLocaleRow(t *testing.T, rows []testutil.LocaleMetadataRow, locale string) testutil.LocaleMetadataRow {
	t.Helper()
	for _, row := range rows {
		if row.Locale == locale {
			return row
		}
	}
	t.Fatalf("locale %s row is missing", locale)
	return testutil.LocaleMetadataRow{}
}

type workVersionRestoreFixture struct {
	db        *gorm.DB
	ctx       context.Context
	systemCtx context.Context
	memberID  string
	manage    *workdomain.WorkService
	internal  *workdomain.InternalWorkService
}

func newWorkVersionRestoreFixture(t *testing.T) *workVersionRestoreFixture {
	t.Helper()
	db := testutil.NewIntegrationDB(t)
	adminID := testutil.IntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, adminID, "Work version restore admin")
	spiceDB := integrationSpiceDB(t)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	sqlDB, err := db.DB()
	require.NoError(t, err)
	publisher, err := mq.NewPublisher(sqlDB)
	require.NoError(t, err)
	writer := apitelemetry.NewDurableWriter(db)
	store, err := contentblock.NewGeneratedStore(newContentBlockFileReuseAuthorizer(spiceDB))
	require.NoError(t, err)
	ctx := withWorkAuditedRequestContext(t, workIntegrationAdminCtx(adminID))
	return &workVersionRestoreFixture{
		db:        db,
		ctx:       ctx,
		systemCtx: withWorkAuditedCollabRequestContext(t, ctx),
		memberID:  testutil.PostIntegrationMemberID(adminID),
		manage: workdomain.NewAuditedWorkService(
			db, newWorkRuntimeForTest(db, ""), spiceDB,
			&fakeIdentityManager{identity: postIntegrationIdentity(adminID, "en")}, publisher, writer,
			workdomain.WithWorkContentBlockStore(store),
			workdomain.WithWorkContentBlockMediaHydrator(passthroughWorkContentBlockMediaHydrator{}),
			workdomain.WithWorkMemberSummaryLoader(workadapter.NewMemberSummaries(db, "")),
		),
		internal: workdomain.NewInternalWorkService(
			db, publisher, newWorkRuntimeForTest(db, ""), spiceDB,
			workdomain.WithInternalWorkDomainAuditWriter(writer),
			workdomain.WithInternalWorkContentBlockStore(store),
			workdomain.WithInternalWorkCheckpoints(testcollaboration.NewCheckpoints(db, spiceDB)),
		),
	}
}

func (f *workVersionRestoreFixture) create(t *testing.T) *workVersionRestoreEntity {
	t.Helper()
	isPresent := true
	created, err := f.manage.CreateWork(f.ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title: workVersionRestoreTitle, Type: managev1.WorkType_WORK_TYPE_ARTICLE,
		Year: 2026, Month: 8, IsPresent: &isPresent, Document: emptyWorkIntegrationDocument("en"),
	}))
	require.NoError(t, err)
	var documentID string
	require.NoError(t, f.db.Table("work").Select("content_document_id::text").
		Where("id = ?", created.Msg.Id).Scan(&documentID).Error)
	return &workVersionRestoreEntity{
		id: created.Msg.Id, documentID: documentID, revision: created.Msg.Revision, blockID: uuid.NewString(),
	}
}

func (f *workVersionRestoreFixture) applyBody(t *testing.T, entity *workVersionRestoreEntity, text string) {
	t.Helper()
	require.NoError(t, f.applyBodyAt(t, entity, entity.revision, text))
}

func (f *workVersionRestoreFixture) applyBodyAt(t *testing.T, entity *workVersionRestoreEntity, revision, text string) error {
	t.Helper()
	applied, err := f.internal.ApplyWorkBlockBatch(f.ctx, connect.NewRequest(&intrav1.ApplyWorkBlockBatchRequest{
		WorkId: entity.id, Locale: "en",
		Batch: testutil.NewParagraphBlockBatch(
			emptyWorkIntegrationDocument("en"), revision, "en", entity.blockID, text, []string{f.memberID},
		),
	}))
	if err != nil {
		return err
	}
	entity.revision = applied.Msg.DocumentRevision
	return nil
}

func (f *workVersionRestoreFixture) updateMetadata(t *testing.T, entity *workVersionRestoreEntity, title string, summary *string) {
	t.Helper()
	request := &intrav1.UpdateWorkLocaleMetadataRequest{
		WorkId: entity.id, Locale: "en", ExpectedRevision: entity.revision,
		ContributorMemberIds: []string{f.memberID}, Title: &title,
	}
	if summary != nil {
		request.SummaryUpdate = &intrav1.UpdateWorkLocaleMetadataRequest_Summary{Summary: *summary}
	}
	updated, err := f.internal.UpdateWorkLocaleMetadata(f.ctx, connect.NewRequest(request))
	require.NoError(t, err)
	entity.revision = updated.Msg.DocumentRevision
}

func (f *workVersionRestoreFixture) checkpoint(t *testing.T, entity *workVersionRestoreEntity) string {
	t.Helper()
	checkpoint, err := f.internal.CreateWorkVersionCheckpoint(f.systemCtx, connect.NewRequest(&intrav1.CreateWorkVersionCheckpointRequest{
		WorkId: entity.id, ExpectedRevision: entity.revision, ContributorMemberIds: []string{f.memberID}, Locale: "en",
	}))
	require.NoError(t, err)
	require.True(t, checkpoint.Msg.Created)
	return checkpoint.Msg.GetVersionId()
}

// switchSourceLocale moves source authority with SQL so the drift does not
// depend on a source-promotion API. The new source locale receives a copy of
// every source overlay, as a promoted translation would have.
func (f *workVersionRestoreFixture) switchSourceLocale(t *testing.T, entity *workVersionRestoreEntity, locale, title string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, f.db.Exec(`
		INSERT INTO content_block_locale (block_id, locale, localized_data)
		SELECT l.block_id, ?, l.localized_data FROM content_block_locale l
		JOIN content_block b ON b.id = l.block_id
		WHERE b.document_id = ?::uuid AND l.locale = 'en'
	`, locale, entity.documentID).Error)
	require.NoError(t, f.db.Exec(`
		INSERT INTO work_translation (entity_id, locale, title, created_at, updated_at)
		VALUES (?::uuid, ?, ?, ?, ?)
	`, entity.id, locale, title, now, now).Error)
	require.NoError(t, f.db.Table("work").Where("id = ?::uuid", entity.id).Update("source_locale", locale).Error)
}

func (f *workVersionRestoreFixture) corruptSnapshotDocument(t *testing.T, versionID string) {
	t.Helper()
	require.NoError(t, f.db.Exec(`
		UPDATE work_version SET content_snapshot = jsonb_set(content_snapshot, '{document}', '{"unknownField": true}')
		WHERE id = ?::uuid
	`, versionID).Error)
}

func (f *workVersionRestoreFixture) clearSnapshotTitle(t *testing.T, versionID string) {
	t.Helper()
	require.NoError(t, f.db.Exec(`
		UPDATE work_version SET content_snapshot = jsonb_set(content_snapshot, '{title}', 'null')
		WHERE id = ?::uuid
	`, versionID).Error)
}

func (f *workVersionRestoreFixture) restore(ctx context.Context, entityID, versionID string) (workVersionRestoreResponse, error) {
	restored, err := f.manage.RestoreWorkVersion(ctx, connect.NewRequest(&managev1.RestoreWorkVersionRequest{
		WorkId: entityID, VersionId: versionID,
	}))
	if err != nil {
		return workVersionRestoreResponse{}, err
	}
	return workVersionRestoreResponse{
		revision: restored.Msg.GetRevision(), title: restored.Msg.GetTitle(),
		summary: restored.Msg.Summary, sourceLocale: restored.Msg.GetSourceLocale(),
	}, nil
}

func (f *workVersionRestoreFixture) sourceLocale(t *testing.T, entity *workVersionRestoreEntity) string {
	t.Helper()
	var locale string
	require.NoError(t, f.db.Table("work").Select("source_locale").Where("id = ?", entity.id).Scan(&locale).Error)
	return locale
}

func (f *workVersionRestoreFixture) updatedAt(t *testing.T, entity *workVersionRestoreEntity) time.Time {
	t.Helper()
	var updatedAt time.Time
	require.NoError(t, f.db.Table("work").Select("updated_at").Where("id = ?", entity.id).Scan(&updatedAt).Error)
	return updatedAt.UTC().Truncate(time.Microsecond)
}

// settingsFingerprint covers the global Work fields a version never restores.
func (f *workVersionRestoreFixture) settingsFingerprint(t *testing.T, entity *workVersionRestoreEntity) string {
	t.Helper()
	var fingerprint string
	require.NoError(t, f.db.Raw(`
		SELECT concat_ws('|', slug, status, type::text, year::text, month::text, is_present::text)
		FROM work WHERE id = ?::uuid
	`, entity.id).Scan(&fingerprint).Error)
	require.NotEmpty(t, fingerprint)
	return fingerprint
}
