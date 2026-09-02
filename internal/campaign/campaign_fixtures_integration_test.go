//go:build integration

package campaign

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type campaignAudienceTargets struct{}

func (campaignAudienceTargets) LockTarget(
	ctx context.Context,
	tx *gorm.DB,
	segmentID string,
) (CampaignAudienceTarget, error) {
	var segment model.AudienceSegment
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&segment, "id = ?", segmentID).Error; err != nil {
		return CampaignAudienceTarget{}, err
	}
	target := CampaignAudienceTarget{
		Archived:    segment.ArchivedAt != nil,
		SegmentType: segment.SegmentType,
	}
	if target.Archived {
		return target, nil
	}
	if err := audience.LoadSegmentConfig(ctx, tx, &segment); err != nil {
		return CampaignAudienceTarget{}, err
	}
	target.Valid = audience.ValidateSegmentConfigForType(segment.SegmentType, segment.Config) == nil
	target.CreatedAfter = segment.CreatedAfter
	target.CreatedBefore = segment.CreatedBefore
	target.MemberTagIDs = append([]string(nil), segment.Config.MemberTagIDs...)
	target.AccountRoles = append([]string(nil), segment.Config.AccountRoles...)
	target.ExcludedMemberIDs = append([]string(nil), segment.Config.ExcludeMemberIDs...)
	return target, nil
}

type campaignAudienceRecipientCounter struct {
	db      *gorm.DB
	spiceDB *auth.SpiceDBClient
}

func (counter campaignAudienceRecipientCounter) Count(
	ctx context.Context,
	segment *model.AudienceSegment,
) (int64, error) {
	return CountBulkEmailRecipientsForAudienceSegment(ctx, counter.db, counter.spiceDB, segment)
}

type campaignAudienceMemberReferences struct{}

func (campaignAudienceMemberReferences) EligibleIDs(
	ctx context.Context,
	db *gorm.DB,
	memberIDs []string,
) ([]string, error) {
	return authorizationtarget.EligibleMemberIDs(ctx, db, memberIDs)
}

func newCampaignAudienceService(
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
) *audience.AudienceService {
	return audience.NewAudienceService(
		db,
		spiceDB,
		campaignAudienceRecipientCounter{db: db, spiceDB: spiceDB},
		campaignAudienceMemberReferences{},
	)
}

func publishCampaignSourceBlocksForIntegration(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	campaignID string,
	text string,
) {
	t.Helper()
	store := testutil.NewEmailContentBlockStore(t, spiceDB)
	documentID, err := loadCampaignEmailContentDocumentID(
		t.Context(), db, campaignContentEntity, campaignID,
	)
	require.NoError(t, err)
	domain, err := loadCampaignEmailSourceContext(
		t.Context(), db, campaignContentEntity, campaignID,
	)
	require.NoError(t, err)
	snapshot, err := store.LoadSnapshot(t.Context(), db, documentID, domain.SourceLocale)
	require.NoError(t, err)
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	require.NoError(t, err)
	contributorID := testutil.IntegrationUUID()
	testutil.InsertAuthorizedDocumentContributor(t, db, spiceDB, contributorID)
	_, err = NewInternalCampaignService(
		db,
		WithInternalCampaignContentBlockStore(store),
		WithInternalCampaignSpiceDB(spiceDB),
		WithInternalCampaignCheckpoints(testcollaboration.NewCheckpoints(db, spiceDB)),
	).ApplyBlockBatch(t.Context(), connect.NewRequest(&intrav1.ApplyCampaignBlockBatchRequest{
		CampaignId: campaignID,
		Locale:     domain.SourceLocale,
		Batch: testutil.NewParagraphBatch(
			document,
			snapshot.Document.Revision.String(),
			domain.SourceLocale,
			text,
			[]string{contributorID},
		),
	}))
	require.NoError(t, err)
}

func seedCampaignActiveMemberEmailPair(
	t *testing.T,
	db *gorm.DB,
	identityID string,
	email string,
) string {
	t.Helper()
	memberID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(
		"UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid",
		memberID,
		identityID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO account_identity (id, created_at)
		 SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid
		 ON CONFLICT (id) DO NOTHING`,
		identityID,
	).Error)
	require.NoError(t, db.Create(&model.Member{
		ID: memberID, AccountIdentityID: &identityID, Nickname: "Campaign recipient " + memberID,
		Onboarded: true, PrimaryEmail: &email, AvailableEmails: []string{email},
		SocialLinks: map[string]string{}, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return memberID
}

type recordingCampaignPublisher struct {
	sendEmailJobs []*managev1.SendEmailEvent
	sendBulkJobs  []*managev1.SendBulkEmailBatchEvent
}

func (publisher *recordingCampaignPublisher) EnqueueProtobuf(
	_ context.Context,
	_ string,
	_ string,
	message proto.Message,
) error {
	if bulk, ok := message.(*managev1.SendBulkEmailBatchEvent); ok {
		publisher.sendBulkJobs = append(publisher.sendBulkJobs, bulk)
	}
	return nil
}

func (*recordingCampaignPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (publisher *recordingCampaignPublisher) EnqueueProtobufWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	queue string,
	messageID string,
	message proto.Message,
) error {
	if executor == nil {
		return errors.New("transactional executor is required")
	}
	return publisher.EnqueueProtobuf(ctx, queue, messageID, message)
}

func (publisher *recordingCampaignPublisher) PublishSendEmail(
	_ context.Context,
	job *managev1.SendEmailEvent,
) error {
	publisher.sendEmailJobs = append(publisher.sendEmailJobs, job)
	return nil
}

func (publisher *recordingCampaignPublisher) PublishSendBulkEmail(
	_ context.Context,
	job *managev1.SendBulkEmailBatchEvent,
) error {
	publisher.sendBulkJobs = append(publisher.sendBulkJobs, job)
	return nil
}

func campaignRequestContext(t *testing.T) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.91")
	require.NoError(t, err)
	return sharedtelemetry.WithRequestContext(t.Context(), requestContext)
}
