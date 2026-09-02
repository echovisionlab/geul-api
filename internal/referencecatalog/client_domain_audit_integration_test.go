//go:build integration

package referencecatalog_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type clientDomainAuditRecord struct {
	Action        string
	TargetType    string `gorm:"column:target_type"`
	TargetID      string `gorm:"column:target_id"`
	ActorMemberID string `gorm:"column:actor_member_id"`
	RequestID     string `gorm:"column:request_id"`
	Attributes    []byte `gorm:"column:attributes"`
}

func TestClientDomainAuditLifecycleIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	adminCtx, spiceDB := testutil.IntegrationAdminContext(t, db)
	admin := auth.GetUser(adminCtx)
	require.NotNil(t, admin)
	identityID, memberID := admin.IdentityID.String(), admin.MemberID.String()
	ctx := testutil.NewAuditContext(t, identityID, memberID)
	service := referencecatalog.NewAuditedClientService(
		db, apitelemetry.NewDurableWriter(db), referencecatalogadapter.NewAssets("https://cdn.example.com"), spiceDB,
	)

	created, err := service.CreateClient(ctx, connect.NewRequest(&managev1.CreateClientRequest{Name: " Client audit lifecycle "}))
	require.NoError(t, err)
	require.Equal(t, "Client audit lifecycle", created.Msg.Name)

	updatedName := "Client audit lifecycle updated"
	updatedWebsite := "https://client-audit.example.test"
	_, err = service.UpdateClient(ctx, connect.NewRequest(&managev1.UpdateClientRequest{
		Id: created.Msg.Id, Name: &updatedName, Website: &updatedWebsite,
	}))
	require.NoError(t, err)
	_, err = service.UpdateClient(ctx, connect.NewRequest(&managev1.UpdateClientRequest{
		Id: created.Msg.Id, Name: &updatedName, Website: &updatedWebsite,
	}))
	require.NoError(t, err)

	lightOriginalID := seedClientLogoFileFixture(t, db, "client-audit/light-original.webp")
	darkID := seedClientLogoFileFixture(t, db, "client-audit/dark.webp")
	lightReplacementID := seedClientLogoFileFixture(t, db, "client-audit/light-replacement.webp")

	// The historical unspecified wire value remains the light slot.
	_, err = service.SetClientLogo(ctx, connect.NewRequest(&managev1.SetClientLogoRequest{
		ClientId: created.Msg.Id, FileId: lightOriginalID,
		Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_UNSPECIFIED,
	}))
	require.NoError(t, err)
	_, err = service.SetClientLogo(ctx, connect.NewRequest(&managev1.SetClientLogoRequest{
		ClientId: created.Msg.Id, FileId: lightOriginalID,
		Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT,
	}))
	require.NoError(t, err)
	_, err = service.SetClientLogo(ctx, connect.NewRequest(&managev1.SetClientLogoRequest{
		ClientId: created.Msg.Id, FileId: darkID,
		Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK,
	}))
	require.NoError(t, err)
	_, err = service.SetClientLogo(ctx, connect.NewRequest(&managev1.SetClientLogoRequest{
		ClientId: created.Msg.Id, FileId: lightReplacementID,
		Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT,
	}))
	require.NoError(t, err)
	_, err = service.DeleteClientLogo(ctx, connect.NewRequest(&managev1.DeleteClientLogoRequest{
		ClientId: created.Msg.Id, Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK,
	}))
	require.NoError(t, err)
	_, err = service.DeleteClientLogo(ctx, connect.NewRequest(&managev1.DeleteClientLogoRequest{
		ClientId: created.Msg.Id, Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT,
	}))
	require.NoError(t, err)
	_, err = service.DeleteClientLogo(ctx, connect.NewRequest(&managev1.DeleteClientLogoRequest{
		ClientId: created.Msg.Id, Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT,
	}))
	require.NoError(t, err)
	_, err = service.DeleteClient(ctx, connect.NewRequest(&managev1.DeleteClientRequest{Id: created.Msg.Id}))
	require.NoError(t, err)

	var records []clientDomainAuditRecord
	require.NoError(t, db.Raw(`
		SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id,
		       request_id::text AS request_id, attributes
		FROM public.domain_audit
		WHERE target_type = 'client'
		ORDER BY occurred_at, audit_id
	`).Scan(&records).Error)
	require.Len(t, records, 8)
	wantAttributes := []string{
		`{}`,
		`{"changed_fields":["name","website"]}`,
		`{"asset_slot":"light","changed_fields":["logo"],"collection_operation":"added","file_id":"` + lightOriginalID + `"}`,
		`{"asset_slot":"dark","changed_fields":["logo"],"collection_operation":"added","file_id":"` + darkID + `"}`,
		`{"asset_slot":"light","changed_fields":["logo"],"collection_operation":"added","file_id":"` + lightReplacementID + `"}`,
		`{"asset_slot":"dark","changed_fields":["logo"],"collection_operation":"removed","file_id":"` + darkID + `"}`,
		`{"asset_slot":"light","changed_fields":["logo"],"collection_operation":"removed","file_id":"` + lightReplacementID + `"}`,
		`{}`,
	}
	wantActions := []string{
		"client.created", "client.updated", "client.updated", "client.updated",
		"client.updated", "client.updated", "client.updated", "client.deleted",
	}
	for index, record := range records {
		require.Equal(t, wantActions[index], record.Action)
		require.Equal(t, "client", record.TargetType)
		require.Equal(t, created.Msg.Id, record.TargetID)
		require.Equal(t, memberID, record.ActorMemberID)
		require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), record.RequestID)
		require.JSONEq(t, wantAttributes[index], string(record.Attributes))
	}

	var retainedFiles int64
	require.NoError(t, db.Table("file").Where("id IN ?", []string{lightOriginalID, darkID, lightReplacementID}).Count(&retainedFiles).Error)
	require.EqualValues(t, 3, retainedFiles)
}

func TestClientDomainAuditFailureAndReferenceRejectionIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	adminCtx, spiceDB := testutil.IntegrationAdminContext(t, db)
	admin := auth.GetUser(adminCtx)
	require.NotNil(t, admin)
	ctx := testutil.NewAuditContext(t, admin.IdentityID.String(), admin.MemberID.String())
	assets := referencecatalogadapter.NewAssets("https://cdn.example.com")

	name := "Client audit rollback"
	_, err := referencecatalog.NewAuditedClientService(db, failingClientAuditAppender{}, assets, spiceDB).CreateClient(
		ctx, connect.NewRequest(&managev1.CreateClientRequest{Name: name}),
	)
	require.Error(t, err)
	var createCount int64
	require.NoError(t, db.Table("client").Where("name = ?", name).Count(&createCount).Error)
	require.Zero(t, createCount)

	base := referencecatalog.NewClientService(db, assets, spiceDB)
	created, err := base.CreateClient(ctx, connect.NewRequest(&managev1.CreateClientRequest{Name: "Client retained on audit failure"}))
	require.NoError(t, err)
	fileID := seedClientLogoFileFixture(t, db, "client-audit/rollback.webp")
	failing := referencecatalog.NewAuditedClientService(db, failingClientAuditAppender{}, assets, spiceDB)
	_, err = failing.SetClientLogo(ctx, connect.NewRequest(&managev1.SetClientLogoRequest{
		ClientId: created.Msg.Id, FileId: fileID, Variant: managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT,
	}))
	require.Error(t, err)
	var logoFileID *string
	require.NoError(t, db.Table("client").Select("logo_light_file_id").Where("id = ?", created.Msg.Id).Scan(&logoFileID).Error)
	require.Nil(t, logoFileID)
	var bindingCount int64
	require.NoError(t, db.Table("public_asset_binding").Where("owner_type = 'client' AND owner_id = ?", created.Msg.Id).Count(&bindingCount).Error)
	require.Zero(t, bindingCount)

	deletable, err := base.CreateClient(ctx, connect.NewRequest(&managev1.CreateClientRequest{Name: "Client delete rollback"}))
	require.NoError(t, err)
	_, err = failing.DeleteClient(ctx, connect.NewRequest(&managev1.DeleteClientRequest{Id: deletable.Msg.Id}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	var rollbackDeleteCount int64
	require.NoError(t, db.Table("client").Where("id = ?", deletable.Msg.Id).Count(&rollbackDeleteCount).Error)
	require.EqualValues(t, 1, rollbackDeleteCount)

	workID := uuid.NewString()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO public.content_document (id, profile) VALUES (?::uuid, 'work')`, documentID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO public.work (id, type, year, month, until_year, until_month, content_document_id)
		 VALUES (?::uuid, 'WORK_TYPE_PORTFOLIO', 2026, 1, 2026, 1, ?::uuid)`, workID, documentID,
	).Error)
	require.NoError(t, db.Exec(`INSERT INTO public.work_client (work_id, client_id) VALUES (?::uuid, ?::uuid)`, workID, created.Msg.Id).Error)
	audited := referencecatalog.NewAuditedClientService(db, apitelemetry.NewDurableWriter(db), assets, spiceDB)
	_, err = audited.DeleteClient(ctx, connect.NewRequest(&managev1.DeleteClientRequest{Id: created.Msg.Id}))
	require.Error(t, err)
	var retainedClientCount int64
	require.NoError(t, db.Table("client").Where("id = ?", created.Msg.Id).Count(&retainedClientCount).Error)
	require.EqualValues(t, 1, retainedClientCount)
	var auditCount int64
	require.NoError(t, db.Table("domain_audit").Where("target_type = 'client' AND target_id = ?", created.Msg.Id).Count(&auditCount).Error)
	require.Zero(t, auditCount)
}

type failingClientAuditAppender struct{}

func (failingClientAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return errors.New("client audit unavailable")
}

var _ domainaudit.Appender = failingClientAuditAppender{}

func seedClientLogoFileFixture(t *testing.T, db *gorm.DB, key string) string {
	t.Helper()
	fileID := uuid.NewString()
	digest := sha256.Sum256([]byte(key))
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: "client-logo-" + fileID, MimeType: "image/webp",
		FileSize: 1024, Extension: "webp", SHA256: digest[:], CreatedAt: time.Now().UTC(),
	}).Error)
	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, "webp")
	require.NoError(t, err)
	now := time.Now().UTC()
	fileSize := int64(1024)
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: "logo", ObjectKey: objectKey,
		Extension: "webp", MimeType: "image/webp", FileSize: &fileSize,
		SHA256: digest[:], Disposition: "inline", Status: model.PublicAssetStatusReady,
		ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return fileID
}
