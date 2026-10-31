package audience

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// CampaignUsage reads and releases the campaigns and delivery runs that
// target a segment. Campaign owns those rows (campaign.SegmentUsage).
type CampaignUsage interface {
	// CountSegmentUsage counts the campaigns and delivery runs of each
	// segment; a segment nothing uses is absent from both maps.
	CountSegmentUsage(ctx context.Context, db *gorm.DB, segmentIDs []string) (campaigns, deliveryRuns map[string]int32, err error)
	// SegmentInActiveUse reports whether a scheduled or sending campaign,
	// and whether a scheduled or sending delivery run, targets the segment.
	SegmentInActiveUse(ctx context.Context, tx *gorm.DB, segmentID string) (campaign, deliveryRun bool, err error)
	// LockSegmentUsage locks the segment's campaigns, then its active
	// delivery runs, and returns what it locked.
	LockSegmentUsage(ctx context.Context, tx *gorm.DB, segmentID string) (campaignIDs, deliveryRunIDs []string, err error)
	// SegmentUsageChanged reports whether the segment's campaigns or active
	// delivery runs differ from the locked ones.
	SegmentUsageChanged(ctx context.Context, tx *gorm.DB, segmentID string, campaignIDs, deliveryRunIDs []string) (bool, error)
	// ReleaseSegmentForArchive cancels the segment's scheduled delivery runs
	// and returns its scheduled campaigns to draft, returning their IDs.
	ReleaseSegmentForArchive(ctx context.Context, tx *gorm.DB, segmentID string, now time.Time) (draftedCampaignIDs []string, err error)
}

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
	Campaigns        CampaignUsage
	Audit            domainaudit.Appender
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "RecipientCounter": d.RecipientCounter == nil,
		"MemberReferences": d.MemberReferences == nil, "Campaigns": d.Campaigns == nil, "Audit": d.Audit == nil,
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
	campaigns        CampaignUsage
	mutations        *mutation.Runner
}

// NewService validates deps and returns the Audience use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db:               deps.DB,
		spiceDB:          deps.SpiceDB,
		recipientCounter: deps.RecipientCounter,
		memberReferences: deps.MemberReferences,
		campaigns:        deps.Campaigns,
		mutations:        mutations,
	}, nil
}

// freshAdmin is the in-transaction authority of every segment mutation.
func freshAdmin(can policyv1.Can) mutation.Access {
	return mutation.FreshAdminCan(can, nil)
}
