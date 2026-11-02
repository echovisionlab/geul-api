package mutation

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newSystemNoteRunner(t *testing.T) (*Runner, *gorm.DB, *audittest.Recorder) {
	t.Helper()
	recorder := &audittest.Recorder{}
	_, db := newTestRunner(t, recorder)
	runner, err := NewSystemStoreRunner(Postgres(db, func(tx *gorm.DB) *gorm.DB { return tx }), recorder)
	require.NoError(t, err)
	return runner, db, recorder
}

func systemNoteSpec(steps *[]string) Spec[note] {
	spec := noteSpec(steps, "system change")
	spec.Access = System()
	spec.Audit = func(note) Audit {
		*steps = append(*steps, "audit")
		return SystemAudit(sharedtelemetry.ServiceBackend, sharedtelemetry.AuditMemberUpdated, roleAuditBuilder())
	}
	return spec
}

func roleAuditBuilder() domainaudit.Builder {
	memberID := uuid.NewString()
	return func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewMemberRoleUpdatedAuditRecord(metadata, memberID, "user", "admin")
	}
}

func TestSystemRunnerKeepsTheSystemActorAndCapabilityWithOtherUnits(t *testing.T) {
	runner, db, recorder := newSystemNoteRunner(t)
	for _, ctx := range []context.Context{t.Context(), memberRequestContext(t)} {
		var steps []string
		_, err := Run(ctx, runner, systemNoteSpec(&steps))
		require.NoError(t, err)
		require.Equal(t, []string{"change", "audit", "outbox"}, steps)
	}
	require.EqualValues(t, 2, noteCount(t, db))
	_, otherDB, _ := newSystemNoteRunner(t)
	other, err := runner.WithUnits(Postgres(otherDB, func(tx *gorm.DB) *gorm.DB { return tx }))
	require.NoError(t, err)
	var steps []string
	_, err = Run(t.Context(), other, systemNoteSpec(&steps))
	require.NoError(t, err)
	require.EqualValues(t, 2, noteCount(t, db))
	require.EqualValues(t, 1, noteCount(t, otherDB))
	records := recorder.Records()
	require.Len(t, records, 3)
	for _, record := range records {
		require.Equal(t, sharedtelemetry.ActorKindSystem, record.Kind)
		require.Equal(t, string(sharedtelemetry.ServiceBackend), record.Service)
		require.Empty(t, record.MemberID)
	}
}

type systemTransactionCounter struct {
	UnitOfWork[*gorm.DB]
	attempts int
}

func (u *systemTransactionCounter) Transaction(ctx context.Context, lockAuthorization bool, work func(Tx[*gorm.DB]) error) error {
	u.attempts++
	return u.UnitOfWork.Transaction(ctx, lockAuthorization, work)
}

func (u *systemTransactionCounter) RelationshipTransaction(ctx context.Context, writer authzmutation.RelationshipWriter, work func(Tx[*gorm.DB], authzmutation.WriteRelationships) error) error {
	u.attempts++
	return u.UnitOfWork.RelationshipTransaction(ctx, writer, work)
}

func TestSystemRunnerRejectsIdentityAuthorityBeforeOpeningATransaction(t *testing.T) {
	for name, edit := range map[string]func(*Spec[note]){
		"principal access":  func(spec *Spec[note]) { spec.Access = FreshCan(policyv1.Post.Edit(uuid.NewString())) },
		"identity subjects": func(spec *Spec[note]) { spec.Subjects = []string{uuid.NewString()} },
		"relationships": func(spec *Spec[note]) {
			spec.Relationships = func(note) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
				return nil, nil, nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, db, recorder := newSystemNoteRunner(t)
			counter := &systemTransactionCounter{UnitOfWork: Postgres(db, func(tx *gorm.DB) *gorm.DB { return tx })}
			runner, setupErr := NewSystemStoreRunner(counter, recorder)
			require.NoError(t, setupErr)
			var steps []string
			spec := systemNoteSpec(&steps)
			edit(&spec)
			_, err := Run(memberRequestContext(t), runner, spec)
			require.ErrorContains(t, err, "system mutation")
			require.Zero(t, counter.attempts)
			require.Empty(t, steps)
			require.Zero(t, noteCount(t, db))
			require.Empty(t, recorder.Records())
		})
	}
}

func TestSystemRunnerRejectsNonSystemAuditBeforeAppendingAnyRecord(t *testing.T) {
	for name, audit := range map[string]Audit{
		"request":   RequestAudit(sharedtelemetry.AuditMemberUpdated, roleAuditBuilder()),
		"member":    MemberAudit(uuid.NewString(), sharedtelemetry.AuditMemberUpdated, roleAuditBuilder()),
		"anonymous": AnonymousAudit(sharedtelemetry.AuditMemberUpdated, roleAuditBuilder()),
		"version":   VersionAudit(sharedtelemetry.AuditMemberUpdated, roleAuditBuilder()),
		"mixed batch": Audits(
			SystemAudit(sharedtelemetry.ServiceBackend, sharedtelemetry.AuditMemberUpdated, roleAuditBuilder()),
			RequestAudit(sharedtelemetry.AuditMemberUpdated, roleAuditBuilder()),
		),
	} {
		t.Run(name, func(t *testing.T) {
			runner, db, recorder := newSystemNoteRunner(t)
			var steps []string
			spec := systemNoteSpec(&steps)
			spec.Audit = func(note) Audit { return audit }
			_, err := Run(memberRequestContext(t), runner, spec)
			require.ErrorContains(t, err, "system mutation requires a system audit")
			require.Equal(t, []string{"change"}, steps)
			require.Zero(t, noteCount(t, db))
			require.Empty(t, recorder.Records())
		})
	}
}

func TestSystemAccessRequiresSystemAuditWithARequestRunner(t *testing.T) {
	runner, db := newTestRunner(t, &audittest.Recorder{})
	var steps []string
	spec := noteSpec(&steps, "wrong actor")
	spec.Access = System()
	_, err := Run(memberRequestContext(t), runner, spec)
	require.ErrorContains(t, err, "system mutation requires a system audit")
	require.Zero(t, noteCount(t, db))
}
