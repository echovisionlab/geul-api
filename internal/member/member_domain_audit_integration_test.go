//go:build integration

package member

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type memberAuditFixture struct {
	IdentityID string
	MemberID   string
}

func createMemberAuditFixture(t *testing.T, db *gorm.DB, nickname string) memberAuditFixture {
	t.Helper()
	fixture := memberAuditFixture{IdentityID: uuid.NewString(), MemberID: uuid.NewString()}
	email := fixture.MemberID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: fixture.IdentityID, Email: email, Name: nickname,
	})
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`UPDATE kratos.identities SET external_id = ?::text WHERE id = ?::uuid`, fixture.MemberID, fixture.IdentityID,
		).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO account_identity (id) VALUES (?::uuid)`, fixture.IdentityID).Error
	}))
	require.NoError(t, db.Exec(`
		INSERT INTO member (
			id, account_identity_id, nickname, onboarded, primary_email, available_emails, social_links
		) VALUES (?::uuid, ?::uuid, ?, TRUE, ?, ARRAY[?::text], '{}'::jsonb)
	`, fixture.MemberID, fixture.IdentityID, nickname, email, email).Error)
	syncIntegrationGlobalRole(t, testutil.SetupOryStack(t).SpiceDBClient, fixture.IdentityID, policyv1.Role.User())
	return fixture
}

func memberAuditContext(t *testing.T, fixture memberAuditFixture) context.Context {
	return memberAuditContextForPair(t, fixture.IdentityID, fixture.MemberID)
}

func memberAuditContextForPair(t *testing.T, identityID, memberID string) context.Context {
	t.Helper()
	return sharedtelemetry.WithActor(
		auditedMemberContext(t, identityID, memberID),
		sharedtelemetry.MemberActor{MemberID: memberID},
	)
}

func memberAuditService(t *testing.T, db *gorm.DB, writer domainaudit.Appender) *MemberService {
	t.Helper()
	return &MemberService{
		db:                   db,
		cdnDomain:            "https://cdn.example.test",
		spicedb:              testutil.SetupOryStack(t).SpiceDBClient,
		accountSummaryReader: integrationAccountSummaryReader{},
		auditWriter:          writer,
	}
}

func memberAuditAttributes(t *testing.T, db *gorm.DB, targetID string, offset int) map[string]any {
	t.Helper()
	var stored struct {
		Attributes []byte `gorm:"column:attributes"`
	}
	require.NoError(t, db.Raw(`
		SELECT attributes
		FROM public.domain_audit
		WHERE action = ? AND target_type = 'member' AND target_id = ?
		ORDER BY occurred_at ASC
		OFFSET ? LIMIT 1
	`, sharedtelemetry.AuditMemberUpdated, targetID, offset).Scan(&stored).Error)
	attributes := map[string]any{}
	require.NoError(t, json.Unmarshal(stored.Attributes, &attributes))
	return attributes
}

func memberAuditCount(t *testing.T, db *gorm.DB, targetID string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("public.domain_audit").
		Where("action = ? AND target_type = 'member' AND target_id = ?", sharedtelemetry.AuditMemberUpdated, targetID).
		Count(&count).Error)
	return count
}

func TestMemberProfileAuditPersistsOnlyApprovedFieldsAndSkipsNoOpIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	fixture := createMemberAuditFixture(t, db, "before")
	service := memberAuditService(t, db, apitelemetry.NewDurableWriter(db))
	ctx := memberAuditContext(t, fixture)
	nickname, bio, website := "after", "approved biography", "https://profile.example.test"

	_, err := service.updateProfile(ctx, fixture.MemberID, &nickname, &bio, &website, map[string]string{"site": website})
	require.NoError(t, err)
	attributes := memberAuditAttributes(t, db, fixture.MemberID, 0)
	require.Equal(t, []any{"bio", "nickname", "social_links", "website"}, attributes["changed_fields"])
	require.Equal(t, nickname, attributes["nickname"])
	require.NotContains(t, attributes, "bio")
	require.NotContains(t, attributes, "website")
	require.NotContains(t, attributes, "social_links")
	require.Equal(t, int64(1), memberAuditCount(t, db, fixture.MemberID))

	_, err = service.updateProfile(ctx, fixture.MemberID, &nickname, &bio, &website, map[string]string{"site": website})
	require.NoError(t, err)
	require.Equal(t, int64(1), memberAuditCount(t, db, fixture.MemberID))

	invalid := " "
	_, err = service.updateProfile(ctx, fixture.MemberID, &invalid, nil, nil, nil)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Equal(t, int64(1), memberAuditCount(t, db, fixture.MemberID))
}

func TestMemberProfileAuditFailureRollsBackIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	fixture := createMemberAuditFixture(t, db, "before")
	service := memberAuditService(t, db, failingDomainAuditAppender{})
	nickname := "must-not-commit"

	_, err := service.updateProfile(memberAuditContext(t, fixture), fixture.MemberID, &nickname, nil, nil, nil)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	var member model.Member
	require.NoError(t, db.Where("id = ?", fixture.MemberID).Take(&member).Error)
	require.Equal(t, "before", member.Nickname)
	require.Zero(t, memberAuditCount(t, db, fixture.MemberID))
}

func TestMemberPreferencesAndTagsAuditPersistExactAttributesIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	fixture := createMemberAuditFixture(t, db, "member")
	service := memberAuditService(t, db, apitelemetry.NewDurableWriter(db))
	ctx := memberAuditContext(t, fixture)
	locale := "ko"
	_, err := service.UpdateMyPreferences(ctx, connect.NewRequest(&managev1.UpdateMyPreferencesRequest{
		PreferredLocale: &locale,
		CookieConsent:   &managev1.CookieConsentUpdate{Analytics: true, Version: 2},
	}))
	require.NoError(t, err)
	preferenceAttributes := memberAuditAttributes(t, db, fixture.MemberID, 0)
	require.Equal(t, []any{"cookie_consent", "preferred_locale"}, preferenceAttributes["changed_fields"])
	require.Equal(t, "ko", preferenceAttributes["preferred_locale"])
	consentID, ok := preferenceAttributes["consent_id"].(string)
	require.True(t, ok)
	var consent model.UserCookieConsent
	require.NoError(t, db.Where("id = ?", consentID).Take(&consent).Error)
	require.Equal(t, fixture.MemberID, consent.MemberID)
	require.True(t, consent.Analytics)

	_, err = service.UpdateMyPreferences(ctx, connect.NewRequest(&managev1.UpdateMyPreferencesRequest{PreferredLocale: &locale}))
	require.NoError(t, err)
	require.Equal(t, int64(1), memberAuditCount(t, db, fixture.MemberID))

	firstTag := model.UserTag{ID: uuid.NewString(), Name: "first"}
	secondTag := model.UserTag{ID: uuid.NewString(), Name: "second"}
	require.NoError(t, db.Create(&firstTag).Error)
	require.NoError(t, db.Create(&secondTag).Error)
	_, err = service.replaceMemberTags(ctx, fixture.MemberID, []string{secondTag.ID, firstTag.ID})
	require.NoError(t, err)
	tagAttributes := memberAuditAttributes(t, db, fixture.MemberID, 1)
	require.Equal(t, []any{"tags"}, tagAttributes["changed_fields"])
	expectedTagIDs := []string{firstTag.ID, secondTag.ID}
	sort.Strings(expectedTagIDs)
	require.Equal(t, []any{expectedTagIDs[0], expectedTagIDs[1]}, tagAttributes["tag_ids"])

	_, err = service.replaceMemberTags(ctx, fixture.MemberID, []string{})
	require.NoError(t, err)
	emptyTagAttributes := memberAuditAttributes(t, db, fixture.MemberID, 2)
	require.Equal(t, []any{}, emptyTagAttributes["tag_ids"])
	_, err = service.replaceMemberTags(ctx, fixture.MemberID, []string{})
	require.NoError(t, err)
	require.Equal(t, int64(3), memberAuditCount(t, db, fixture.MemberID))
}

func TestMemberPreferencesAuditFailureRollsBackIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	fixture := createMemberAuditFixture(t, db, "member")
	service := memberAuditService(t, db, failingDomainAuditAppender{})
	locale := "ko"

	_, err := service.UpdateMyPreferences(memberAuditContext(t, fixture), connect.NewRequest(&managev1.UpdateMyPreferencesRequest{
		PreferredLocale: &locale,
		CookieConsent:   &managev1.CookieConsentUpdate{Analytics: true, Version: 2},
	}))
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	var member model.Member
	require.NoError(t, db.Where("id = ?", fixture.MemberID).Take(&member).Error)
	require.Nil(t, member.PreferredLocale)
	var consentCount int64
	require.NoError(t, db.Model(&model.UserCookieConsent{}).Where("member_id = ?", fixture.MemberID).Count(&consentCount).Error)
	require.Zero(t, consentCount)
	require.Zero(t, memberAuditCount(t, db, fixture.MemberID))
}

func TestMemberTagAuditsCommitExactLifecycleAndSkipRejectedMutationsIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	admin := createMemberAuditFixture(t, db, "member-tag-admin")
	firstMember := createMemberAuditFixture(t, db, "member-tag-first")
	secondMember := createMemberAuditFixture(t, db, "member-tag-second")
	service := memberAuditService(t, db, apitelemetry.NewDurableWriter(db))
	syncIntegrationGlobalRole(t, testutil.SetupOryStack(t).SpiceDBClient, admin.IdentityID, policyv1.Role.Admin())
	ctx := memberAuditContext(t, admin)

	created, err := service.CreateMemberTag(ctx, connect.NewRequest(&managev1.CreateMemberTagRequest{Name: "  Featured members  "}))
	require.NoError(t, err)
	require.Equal(t, "Featured members", created.Msg.Name)
	assertMemberTagAudit(t, db, sharedtelemetry.AuditMemberTagCreated, created.Msg.Id, admin.MemberID, "Featured members")
	_, err = service.CreateMemberTag(ctx, connect.NewRequest(&managev1.CreateMemberTagRequest{Name: "Featured members"}))
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	var sameNameTagCount int64
	require.NoError(t, db.Model(&model.UserTag{}).Where("name = ?", "Featured members").Count(&sameNameTagCount).Error)
	require.Equal(t, int64(1), sameNameTagCount)

	require.NoError(t, db.Create(&[]model.UserTagMapping{
		{MemberID: firstMember.MemberID, TagID: created.Msg.Id},
		{MemberID: secondMember.MemberID, TagID: created.Msg.Id},
	}).Error)
	_, err = service.DeleteMemberTag(ctx, connect.NewRequest(&managev1.DeleteMemberTagRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	assertMemberTagAudit(t, db, sharedtelemetry.AuditMemberTagDeleted, created.Msg.Id, admin.MemberID, "Featured members")
	var mappingCount int64
	require.NoError(t, db.Model(&model.UserTagMapping{}).Where("tag_id = ?", created.Msg.Id).Count(&mappingCount).Error)
	require.Zero(t, mappingCount)
	var memberAuditRows int64
	require.NoError(t, db.Table("public.domain_audit").Where("action = ?", sharedtelemetry.AuditMemberUpdated).Count(&memberAuditRows).Error)
	require.Zero(t, memberAuditRows)

	_, err = service.CreateMemberTag(ctx, connect.NewRequest(&managev1.CreateMemberTagRequest{Name: "  "}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = service.DeleteMemberTag(ctx, connect.NewRequest(&managev1.DeleteMemberTagRequest{Id: uuid.NewString()}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	var remainingTagCount, auditCount int64
	require.NoError(t, db.Model(&model.UserTag{}).Count(&remainingTagCount).Error)
	require.Zero(t, remainingTagCount)
	require.NoError(t, db.Table("public.domain_audit").Count(&auditCount).Error)
	require.Equal(t, int64(2), auditCount)
}

func TestMemberTagDeleteRejectsAudienceReferencesWithoutAuditIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	admin := createMemberAuditFixture(t, db, "member-tag-audience-admin")
	service := memberAuditService(t, db, apitelemetry.NewDurableWriter(db))
	syncIntegrationGlobalRole(t, testutil.SetupOryStack(t).SpiceDBClient, admin.IdentityID, policyv1.Role.Admin())
	ctx := memberAuditContext(t, admin)

	tag, err := service.CreateMemberTag(ctx, connect.NewRequest(&managev1.CreateMemberTagRequest{Name: "Audience retained"}))
	require.NoError(t, err)
	segment := model.AudienceSegment{ID: uuid.NewString(), Name: "Tagged audience", SegmentType: "SEGMENT_TYPE_MEMBER_TAGS"}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&segment).Error; err != nil {
			return err
		}
		return tx.Create(&model.AudienceSegmentUserTag{AudienceSegmentID: segment.ID, UserTagID: tag.Msg.Id}).Error
	}))

	_, err = service.DeleteMemberTag(ctx, connect.NewRequest(&managev1.DeleteMemberTagRequest{Id: tag.Msg.Id}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	var tagCount, auditCount int64
	require.NoError(t, db.Model(&model.UserTag{}).Where("id = ?", tag.Msg.Id).Count(&tagCount).Error)
	require.Equal(t, int64(1), tagCount)
	var referenceCount int64
	require.NoError(t, db.Model(&model.AudienceSegmentUserTag{}).Where("user_tag_id = ?", tag.Msg.Id).Count(&referenceCount).Error)
	require.Equal(t, int64(1), referenceCount)
	require.NoError(t, db.Table("public.domain_audit").Count(&auditCount).Error)
	require.Equal(t, int64(1), auditCount)
}

func TestMemberTagAuditAppendFailureRollsBackCreateAndDeleteIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	admin := createMemberAuditFixture(t, db, "member-tag-failing-admin")
	assignedMember := createMemberAuditFixture(t, db, "member-tag-failing-member")
	service := memberAuditService(t, db, failingDomainAuditAppender{})
	syncIntegrationGlobalRole(t, testutil.SetupOryStack(t).SpiceDBClient, admin.IdentityID, policyv1.Role.Admin())
	ctx := memberAuditContext(t, admin)

	_, err := service.CreateMemberTag(ctx, connect.NewRequest(&managev1.CreateMemberTagRequest{Name: "Must roll back"}))
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	var createdCount int64
	require.NoError(t, db.Model(&model.UserTag{}).Where("name = ?", "Must roll back").Count(&createdCount).Error)
	require.Zero(t, createdCount)

	tag := model.UserTag{ID: uuid.NewString(), Name: "Delete must roll back"}
	require.NoError(t, db.Create(&tag).Error)
	require.NoError(t, db.Create(&model.UserTagMapping{MemberID: assignedMember.MemberID, TagID: tag.ID}).Error)
	_, err = service.DeleteMemberTag(ctx, connect.NewRequest(&managev1.DeleteMemberTagRequest{Id: tag.ID}))
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	var retainedTagCount, retainedMappingCount, auditCount int64
	require.NoError(t, db.Model(&model.UserTag{}).Where("id = ?", tag.ID).Count(&retainedTagCount).Error)
	require.Equal(t, int64(1), retainedTagCount)
	require.NoError(t, db.Model(&model.UserTagMapping{}).Where("tag_id = ?", tag.ID).Count(&retainedMappingCount).Error)
	require.Equal(t, int64(1), retainedMappingCount)
	require.NoError(t, db.Table("public.domain_audit").Count(&auditCount).Error)
	require.Zero(t, auditCount)
}

func assertMemberTagAudit(t *testing.T, db *gorm.DB, action sharedtelemetry.AuditAction, tagID, actorMemberID, tagName string) {
	t.Helper()
	var record struct {
		TargetType    string `gorm:"column:target_type"`
		TargetID      string `gorm:"column:target_id"`
		ActorMemberID string `gorm:"column:actor_member_id"`
		Attributes    []byte `gorm:"column:attributes"`
	}
	require.NoError(t, db.Raw(`
		SELECT target_type, target_id, actor_member_id::text AS actor_member_id, attributes
		FROM public.domain_audit
		WHERE action = ? AND target_id = ?
	`, action, tagID).Scan(&record).Error)
	require.Equal(t, "member_tag", record.TargetType)
	require.Equal(t, tagID, record.TargetID)
	require.Equal(t, actorMemberID, record.ActorMemberID)
	attributes := map[string]any{}
	require.NoError(t, json.Unmarshal(record.Attributes, &attributes))
	require.Equal(t, map[string]any{"tag_name": tagName}, attributes)
}

type memberAuditAvatarPromoter struct{ assetID string }

func (memberAuditAvatarPromoter) DeleteFileByID(context.Context, string) error { return nil }

func (p memberAuditAvatarPromoter) PromoteUserAvatarAsset(context.Context, string) (*commonv1.AssetRef, error) {
	return &commonv1.AssetRef{AssetId: p.assetID}, nil
}

func TestMemberAvatarAuditRollbackAndNoOpIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	fixture := createMemberAuditFixture(t, db, "member")
	fileID, assetID := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	size := int64(1)
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: "avatar", MimeType: "image/webp", FileSize: size, Extension: "webp", SHA256: make([]byte, 32),
	}).Error)
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, Kind: "avatar", ObjectKey: "asset/" + assetID + ".webp", Extension: "webp", MimeType: "image/webp",
		FileSize: &size, SHA256: make([]byte, 32), Disposition: "inline", Status: model.PublicAssetStatusReady, ReadyAt: &now,
	}).Error)
	require.NoError(t, db.Create(&model.FileIngestBinding{FileID: fileID, UploadType: managev1.UploadType_UPLOAD_TYPE_USER_AVATAR.String(), EntityID: fixture.MemberID}).Error)
	service := memberAuditService(t, db, apitelemetry.NewDurableWriter(db))
	service.fileService = memberAuditAvatarPromoter{assetID: assetID}
	ctx := memberAuditContext(t, fixture)

	_, err := service.setAvatar(ctx, fixture.MemberID, fileID, false)
	require.NoError(t, err)
	attributes := memberAuditAttributes(t, db, fixture.MemberID, 0)
	require.Equal(t, []any{"avatar"}, attributes["changed_fields"])
	require.Equal(t, "added", attributes["collection_operation"])
	require.Equal(t, assetID, attributes["asset_id"])
	_, err = service.setAvatar(ctx, fixture.MemberID, fileID, false)
	require.NoError(t, err)
	require.Equal(t, int64(1), memberAuditCount(t, db, fixture.MemberID))

	failing := memberAuditService(t, db, failingDomainAuditAppender{})
	failing.fileService = memberAuditAvatarPromoter{assetID: assetID}
	_, err = failing.deleteAvatar(ctx, fixture.MemberID, false)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	var binding model.PublicAssetBinding
	require.NoError(t, db.Where("owner_type = 'member' AND owner_id = ? AND binding_key = 'avatar'", fixture.MemberID).Take(&binding).Error)
	require.Equal(t, assetID, binding.AssetID)

	_, err = service.deleteAvatar(ctx, fixture.MemberID, false)
	require.NoError(t, err)
	removedAttributes := memberAuditAttributes(t, db, fixture.MemberID, 1)
	require.Equal(t, "removed", removedAttributes["collection_operation"])
	require.Equal(t, assetID, removedAttributes["asset_id"])
	_, err = service.deleteAvatar(ctx, fixture.MemberID, false)
	require.NoError(t, err)
	require.Equal(t, int64(2), memberAuditCount(t, db, fixture.MemberID))
}
