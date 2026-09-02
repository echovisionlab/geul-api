//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestWorkSourceEditAndSwitchPreserveTargetsAndJobsIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Work source pointer Admin")
	ctx := workIntegrationAdminCtx(adminID)
	workService := newWorkIntegrationService(t, db, adminID, struct{}{})
	present := true
	created, err := workService.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title: "Work pointer source", Type: managev1.WorkType_WORK_TYPE_ARTICLE,
		Year: 2026, Month: 8, IsPresent: &present, Document: emptyWorkIntegrationDocument("en"),
	}))
	require.NoError(t, err)

	var initialSource struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	require.NoError(t, db.Table("work").Select("source_locale").Where("id = ?", created.Msg.Id).Take(&initialSource).Error)
	seedWorkPointerTarget(t, db, created.Msg.Id, "ko", "Stored Korean target")
	seedWorkPointerTarget(t, db, created.Msg.Id, "fr", "Stored French target")
	requesterMemberID := integrationMemberID(adminID)
	seedWorkPointerJob(t, db, created.Msg.Id, "ko", "queued", initialSource.SourceLocale, requesterMemberID)
	seedWorkPointerJob(t, db, created.Msg.Id, "fr", "running", initialSource.SourceLocale, requesterMemberID)

	koBeforeEdit := loadWorkPointerLocale(t, db, created.Msg.Id, "ko")
	frBeforeEdit := loadWorkPointerLocale(t, db, created.Msg.Id, "fr")
	jobsBeforeEdit := loadWorkPointerJobs(t, db, created.Msg.Id)
	requireWorkPointerLocaleMissing(t, db, created.Msg.Id, "ja")

	blockStore, err := contentblock.NewGeneratedStore(newContentBlockFileReuseAuthorizer(integrationSpiceDB(t)))
	require.NoError(t, err)
	internalWork := workdomain.NewInternalWorkService(
		db, noopAsyncPublisher{}, newWorkRuntimeForTest(db, ""), integrationSpiceDB(t),
		workdomain.WithInternalWorkDomainAuditWriter(apitelemetry.NewDurableWriter(db)),
		workdomain.WithInternalWorkContentBlockStore(blockStore),
		workdomain.WithInternalWorkCheckpoints(testcollaboration.NewCheckpoints(db, integrationSpiceDB(t))),
	)
	editedTitle := "Work pointer source edited"
	edited, err := internalWork.UpdateWorkLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdateWorkLocaleMetadataRequest{
		WorkId: created.Msg.Id, Locale: "en", Title: &editedTitle, ExpectedRevision: created.Msg.Revision,
		ContributorMemberIds: []string{integrationMemberID(adminID)},
	}))
	require.NoError(t, err)
	require.True(t, edited.Msg.SourceChanged)
	require.Equal(t, koBeforeEdit, loadWorkPointerLocale(t, db, created.Msg.Id, "ko"))
	require.Equal(t, frBeforeEdit, loadWorkPointerLocale(t, db, created.Msg.Id, "fr"))
	require.Equal(t, jobsBeforeEdit, loadWorkPointerJobs(t, db, created.Msg.Id))
	requireWorkPointerLocaleMissing(t, db, created.Msg.Id, "ja")

	enBeforeSwitch := loadWorkPointerLocale(t, db, created.Msg.Id, "en")
	frBeforeSwitch := loadWorkPointerLocale(t, db, created.Msg.Id, "fr")
	jobsBeforeSwitch := loadWorkPointerJobs(t, db, created.Msg.Id)
	versionTitle := "Restored Korean source"
	versionSummary := "Restored Korean summary"
	versionDocument := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
		Locale:                  "ko",
		Base:                    &contentv1.RichTextBlockGraph{},
		LocaleOverlay:           &contentv1.RichTextLocaleOverlay{Locale: "ko"},
	}
	encoded, err := workdomain.EncodeVersionContentSnapshot("ko", &versionTitle, &versionSummary, versionDocument)
	require.NoError(t, err)
	version := model.WorkVersion{
		WorkID: created.Msg.Id, Version: 1, Title: &versionTitle, Summary: &versionSummary,
		ContentSnapshot: encoded, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, db.Create(&version).Error)

	_, err = workService.RestoreWorkVersion(ctx, connect.NewRequest(&managev1.RestoreWorkVersionRequest{
		WorkId: created.Msg.Id, VersionId: version.ID,
	}))
	require.NoError(t, err)
	require.Equal(t, enBeforeSwitch, loadWorkPointerLocale(t, db, created.Msg.Id, "en"))
	require.Equal(t, frBeforeSwitch, loadWorkPointerLocale(t, db, created.Msg.Id, "fr"))
	require.Equal(t, jobsBeforeSwitch, loadWorkPointerJobs(t, db, created.Msg.Id))
	requireWorkPointerLocaleMissing(t, db, created.Msg.Id, "ja")

	var switched struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	require.NoError(t, db.Table("work").Select("source_locale").Where("id = ?", created.Msg.Id).Take(&switched).Error)
	require.Equal(t, "ko", switched.SourceLocale)
}

type workPointerLocaleSnapshot struct {
	Locale    string    `gorm:"column:locale"`
	Title     *string   `gorm:"column:title"`
	Summary   *string   `gorm:"column:summary"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func seedWorkPointerTarget(
	t *testing.T,
	db *gorm.DB,
	workID string,
	locale string,
	title string,
) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO work_translation (
			entity_id, locale, title, created_at, updated_at
		) VALUES (?::uuid, ?, ?, ?, ?)
	`, workID, locale, title, now, now).Error)
}

func loadWorkPointerLocale(t *testing.T, db *gorm.DB, workID string, locale string) workPointerLocaleSnapshot {
	t.Helper()
	var row workPointerLocaleSnapshot
	require.NoError(t, db.Table("work_translation").
		Select(`locale, title, summary, created_at, updated_at`).
		Where("entity_id = ? AND locale = ?", workID, locale).Take(&row).Error)
	return row
}

func requireWorkPointerLocaleMissing(t *testing.T, db *gorm.DB, workID string, locale string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("work_translation").
		Where("entity_id = ? AND locale = ?", workID, locale).Count(&count).Error)
	require.Zero(t, count)
}

type workPointerJobSnapshot struct {
	ID        string    `gorm:"column:id"`
	Status    string    `gorm:"column:status"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func seedWorkPointerJob(
	t *testing.T,
	db *gorm.DB,
	workID string,
	targetLocale string,
	status string,
	sourceLocale string,
	requesterMemberID string,
) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
	INSERT INTO translation_job (
			id, entity_type, entity_id, target_locale, source_locale, request_artifact_digest,
			operation_id, status, request_xliff, request_manifest, requested_by_member_id,
			requested_at, created_at, updated_at
		) VALUES (?::uuid, 'work', ?, ?, ?, ?, ?, ?, ?, '{}'::jsonb, ?::uuid, ?, ?, ?)
	`, integrationTestUUID(), workID, targetLocale, sourceLocale, strings.Repeat("0", 63)+"1",
		integrationTestUUID(), status,
		[]byte(`<?xml version="1.0" encoding="UTF-8"?><xliff xmlns="urn:oasis:names:tc:xliff:document:2.2" version="2.2"></xliff>`),
		requesterMemberID, now, now, now).Error)
}

func loadWorkPointerJobs(t *testing.T, db *gorm.DB, workID string) []workPointerJobSnapshot {
	t.Helper()
	var rows []workPointerJobSnapshot
	require.NoError(t, db.Table("translation_job").
		Select(`id::text AS id, status, updated_at`).
		Where("entity_type = 'work' AND entity_id = ?", workID).
		Order("id").Scan(&rows).Error)
	return rows
}
