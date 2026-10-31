//go:build integration

package referencecatalog

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	menudomain "github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var errUnusedTestDependency = errors.New("test did not configure this Reference Catalog dependency")

// newTestService builds the Reference Catalog use cases through NewService.
// Unset collaborators get inert assets, members that fail when reached, the
// menu target lifecycle, Work counts read from work_client and a recording
// audit appender.
func newTestService(t *testing.T, deps Deps) *Service {
	t.Helper()
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	if deps.Assets == nil {
		deps.Assets = referenceCatalogTestAssets{}
	}
	if deps.Members == nil {
		deps.Members = unusedMemberSummaries{}
	}
	if deps.MenuTargets == nil {
		deps.MenuTargets = referenceMenuTargetsForTest(deps.Audit)
	}
	if deps.ClientWorks == nil {
		deps.ClientWorks = referenceClientWorks{}
	}
	service, err := NewService(deps)
	require.NoError(t, err)
	return service
}

type unusedMemberSummaries struct{}

func (unusedMemberSummaries) Resolve(context.Context, *gorm.DB, []string) (map[string]*commonv1.MemberSummary, error) {
	return nil, errUnusedTestDependency
}

type referenceClientWorks struct{}

func (referenceClientWorks) CountByClient(ctx context.Context, db *gorm.DB, clientIDs []string) (map[string]int32, error) {
	var rows []struct {
		ClientID string
		Count    int32
	}
	if err := db.WithContext(ctx).Table("work_client").Select("client_id, COUNT(*) AS count").
		Where("client_id IN ?", clientIDs).Group("client_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int32, len(rows))
	for _, row := range rows {
		counts[row.ClientID] = row.Count
	}
	return counts, nil
}

// adminRequestContext is an administrator request with the request context a
// request-attributed audit record needs.
func adminRequestContext(t *testing.T, db *gorm.DB) (context.Context, *auth.SpiceDBClient) {
	t.Helper()
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	return referenceAuditedRequestContext(t, ctx), spiceDB
}

type referenceMenuTargetsTestAdapter struct {
	lifecycle menudomain.TargetLifecycle
}

type referenceCatalogTestAssets struct{}

func (referenceCatalogTestAssets) LockForAttachment(context.Context, *gorm.DB, []string) error {
	return nil
}

func (referenceCatalogTestAssets) BindReady(
	context.Context,
	*gorm.DB,
	AssetBinding,
) (*commonv1.AssetRef, error) {
	return nil, nil
}

func (referenceCatalogTestAssets) Release(context.Context, *gorm.DB, AssetRelease) error {
	return nil
}

func (referenceCatalogTestAssets) ReadyRef(
	context.Context,
	*gorm.DB,
	AssetSource,
) (*commonv1.AssetRef, error) {
	return nil, nil
}

func referenceMenuTargetsForTest(writer domainaudit.Appender) MenuTargets {
	return referenceMenuTargetsTestAdapter{lifecycle: menudomain.NewTargetLifecycle(writer)}
}

func (adapter referenceMenuTargetsTestAdapter) UpdateSlug(
	ctx context.Context,
	db *gorm.DB,
	change MenuTargetSlugChange,
) error {
	return adapter.lifecycle.UpdateSlug(
		ctx,
		db,
		change.Target.LinkType,
		change.Target.ID,
		change.Target.Slug,
		change.NextSlug,
	)
}

func (adapter referenceMenuTargetsTestAdapter) Remove(
	ctx context.Context,
	db *gorm.DB,
	target MenuTarget,
) error {
	return adapter.lifecycle.Remove(ctx, db, target.LinkType, target.ID, target.Slug)
}

func referenceAuditedRequestContext(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	user := auth.GetUser(ctx)
	require.NotNil(t, user)
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.71")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(ctx, requestContext), user)
}

type referenceFailingAuditAppender struct{}

func (referenceFailingAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return errors.New("reference audit unavailable")
}

func seedReferencePostMapping(
	t *testing.T,
	db *gorm.DB,
	mappingTable string,
	referenceColumn string,
	referenceID string,
) func() {
	t.Helper()
	documentID := testutil.IntegrationUUID()
	postID := testutil.IntegrationUUID()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile) VALUES (?::uuid, 'post')`, documentID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO post (id, content_document_id) VALUES (?::uuid, ?::uuid)`, postID, documentID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO `+mappingTable+` (post_id, `+referenceColumn+`) VALUES (?::uuid, ?::uuid)`,
		postID,
		referenceID,
	).Error)
	return func() {
		require.NoError(t, db.Exec(`DELETE FROM `+mappingTable+` WHERE post_id = ?::uuid`, postID).Error)
		require.NoError(t, db.Exec(`DELETE FROM post WHERE id = ?::uuid`, postID).Error)
		require.NoError(t, db.Exec(`DELETE FROM content_document WHERE id = ?::uuid`, documentID).Error)
	}
}

func seedReferenceReleaseMapping(
	t *testing.T,
	db *gorm.DB,
	mappingTable string,
	referenceColumn string,
	referenceID string,
) func() {
	t.Helper()
	documentID := testutil.IntegrationUUID()
	releaseID := testutil.IntegrationUUID()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile) VALUES (?::uuid, 'compact')`, documentID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO release (id, type, content_document_id) VALUES (?::uuid, 'RELEASE_TYPE_ALBUM', ?::uuid)`,
		releaseID,
		documentID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO `+mappingTable+` (release_id, `+referenceColumn+`) VALUES (?::uuid, ?::uuid)`,
		releaseID,
		referenceID,
	).Error)
	return func() {
		require.NoError(t, db.Exec(`DELETE FROM `+mappingTable+` WHERE release_id = ?::uuid`, releaseID).Error)
		require.NoError(t, db.Exec(`DELETE FROM release WHERE id = ?::uuid`, releaseID).Error)
		require.NoError(t, db.Exec(`DELETE FROM content_document WHERE id = ?::uuid`, documentID).Error)
	}
}

func seedReferenceMenuTarget(
	t *testing.T,
	db *gorm.DB,
	name string,
	item model.MenuItem,
) string {
	t.Helper()
	items, err := json.Marshal([]model.MenuItem{item})
	require.NoError(t, err)
	now := time.Now().UTC()
	documentID := testutil.IntegrationUUID()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, created_at, updated_at)
		 VALUES (?::uuid, 'compact', ?, ?)`,
		documentID,
		now,
		now,
	).Error)
	menu := model.Menu{
		ID:                testutil.IntegrationUUID(),
		ContentDocumentID: documentID,
		SourceLocale:      "en",
		Name:              name,
		Items:             items,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	require.NoError(t, db.Create(&menu).Error)
	return menu.ID
}

func requireNoRow(t *testing.T, db *gorm.DB, tableName string, id string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table(tableName).Where("id = ?", id).Count(&count).Error)
	require.Zero(t, count)
}

var _ domainaudit.Appender = referenceFailingAuditAppender{}
