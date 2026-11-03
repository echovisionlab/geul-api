package criteria_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	"github.com/echovisionlab/geul-api/internal/member/criteria/criteriatest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// harness is the Criteria service over the in-memory Store and the
// authorization, fence and audit stand-ins.
type harness struct {
	store   *criteriatest.Store
	units   *mutationtest.UnitOfWork[membercriteria.Store]
	checker *authztest.Checker
	fence   *identitystatetest.Fence
	audit   *audittest.Recorder
	counter *recipientCounter
	service *membercriteria.Service
	// admin is an onboarded principal's request; whether it holds
	// administrator authority is the checker's rule.
	admin      context.Context
	identityID string
	memberID   string
}

type recipientCounter struct {
	count       int64
	err         error
	criteriaSet []membercriteria.Criteria
}

func (c *recipientCounter) Count(_ context.Context, criteria membercriteria.Criteria) (int64, error) {
	c.criteriaSet = append(c.criteriaSet, criteria)
	return c.count, c.err
}

// newHarness allows every check unless checker is given.
func newHarness(t *testing.T, checker ...*authztest.Checker) *harness {
	t.Helper()
	store, units := criteriatest.New()
	h := &harness{
		store: store, units: units, checker: authztest.AllowAll(), fence: &identitystatetest.Fence{},
		audit: &audittest.Recorder{}, counter: &recipientCounter{},
		identityID: uuid.NewString(), memberID: uuid.NewString(),
	}
	if len(checker) == 1 {
		h.checker = checker[0]
	}
	service, err := membercriteria.NewService(membercriteria.Deps{
		Units: units, SpiceDB: h.checker, Fence: h.fence, Audit: h.audit, RecipientCounter: h.counter,
	})
	require.NoError(t, err)
	h.service = service
	h.admin = principalContext(t, h.identityID, h.memberID)
	return h
}

func principalContext(t *testing.T, identityID, memberID string) context.Context {
	t.Helper()
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{
		IdentityID: identityID, MemberID: memberID, SessionID: uuid.NewString(),
	})
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), request), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
}

// lastStep is the end of the last transaction.
func (h *harness) lastStep() mutation.Step {
	steps := h.units.Steps()
	if len(steps) == 0 {
		return ""
	}
	return steps[len(steps)-1]
}

// auditRecord is what a test asserts of an audit record.
type auditRecord struct {
	Action          sharedtelemetry.AuditAction
	TargetType      string
	TargetID        string
	ItemID          string
	FileID          string
	ChangedFields   []string
	PreviousState   sharedtelemetry.AuditState
	NewState        sharedtelemetry.AuditState
	PreviousItemIDs []string
	ItemIDs         []string
}

func (h *harness) auditRecords() []auditRecord {
	records := h.audit.Records()
	summaries := make([]auditRecord, len(records))
	for i, record := range records {
		summaries[i] = auditRecord{
			Action: record.Action, TargetType: record.TargetType, TargetID: record.TargetID,
			ItemID: record.ItemID, FileID: record.FileID, ChangedFields: record.ChangedFields,
			PreviousState: record.PreviousState, NewState: record.NewState,
		}
		if record.PreviousItemIDs != nil {
			summaries[i].PreviousItemIDs = *record.PreviousItemIDs
		}
		if record.ItemIDs != nil {
			summaries[i].ItemIDs = *record.ItemIDs
		}
	}
	return summaries
}

func pointer[T any](value T) *T { return &value }
