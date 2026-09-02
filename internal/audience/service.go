package audience

import (
	"context"
	"reflect"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

const (
	deliveryRunScheduled = "scheduled"
	deliveryRunSending   = "sending"
	deliveryRunCancelled = "cancelled"
)

var (
	manageCampaignScheduled = managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String()
	manageCampaignSending   = managev1.CampaignStatus_CAMPAIGN_STATUS_SENDING.String()
	manageCampaignDraft     = managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String()
)

// RecipientCounter supplies the authoritative recipient estimate for an
// audience segment.
type RecipientCounter interface {
	Count(context.Context, *model.AudienceSegment) (int64, error)
}

// MemberReferences validates durable member references before they are
// persisted in an audience segment.
type MemberReferences interface {
	EligibleIDs(context.Context, *gorm.DB, []string) ([]string, error)
}

// AudienceService implements the Audience Connect handler.
type AudienceService struct {
	managev1connect.UnimplementedAudienceServiceHandler
	db               *gorm.DB
	spiceDB          *auth.SpiceDBClient
	recipientCounter RecipientCounter
	memberReferences MemberReferences
	auditWriter      domainaudit.Appender
}

// NewAudienceService constructs an audience service with its required
// consumer-owned persistence seams.
func NewAudienceService(
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	recipientCounter RecipientCounter,
	memberReferences MemberReferences,
) *AudienceService {
	if db == nil {
		panic("db is required")
	}
	if spiceDB == nil {
		panic("spiceDB is required")
	}
	if dependencyIsNil(recipientCounter) {
		panic("recipient counter is required")
	}
	if dependencyIsNil(memberReferences) {
		panic("member references are required")
	}
	return &AudienceService{
		db:               db,
		spiceDB:          spiceDB,
		recipientCounter: recipientCounter,
		memberReferences: memberReferences,
	}
}

func dependencyIsNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// NewAuditedAudienceService constructs an audience service that appends
// authoritative domain audit records in the surrounding database transaction.
func NewAuditedAudienceService(
	db *gorm.DB,
	auditWriter domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
	recipientCounter RecipientCounter,
	memberReferences MemberReferences,
) *AudienceService {
	if auditWriter == nil {
		panic("audience audit writer is required")
	}
	service := NewAudienceService(db, spiceDB, recipientCounter, memberReferences)
	service.auditWriter = auditWriter
	return service
}
