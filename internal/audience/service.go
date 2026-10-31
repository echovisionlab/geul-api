package audience

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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

// Deps are the collaborators of the Audience use cases. Every field is
// required; NewService rejects a missing one.
type Deps struct {
	DB      *gorm.DB
	SpiceDB *auth.SpiceDBClient
	// RecipientCounter estimates the recipients of a segment.
	RecipientCounter RecipientCounter
	// MemberReferences validates excluded Members before they are stored.
	MemberReferences MemberReferences
	Audit            domainaudit.Appender
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "RecipientCounter": d.RecipientCounter == nil,
		"MemberReferences": d.MemberReferences == nil, "Audit": d.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("audience dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Audience use cases. The Connect adapter translates its
// requests into their commands and queries.
type Service struct {
	db               *gorm.DB
	spiceDB          *auth.SpiceDBClient
	recipientCounter RecipientCounter
	memberReferences MemberReferences
	auditWriter      domainaudit.Appender
}

// NewService validates deps and returns the Audience use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{
		db:               deps.DB,
		spiceDB:          deps.SpiceDB,
		recipientCounter: deps.RecipientCounter,
		memberReferences: deps.MemberReferences,
		auditWriter:      deps.Audit,
	}, nil
}
