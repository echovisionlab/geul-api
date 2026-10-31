package mutation

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type note struct {
	ID   uint `gorm:"primaryKey"`
	Text string
}

func newTestRunner(t *testing.T, recorder *audittest.Recorder) (*Runner, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&note{}))
	runner, err := NewRunner(db, &auth.SpiceDBClient{}, recorder)
	require.NoError(t, err)
	return runner, db
}

func memberRequestContext(t *testing.T) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.10")
	require.NoError(t, err)
	ctx := sharedtelemetry.WithRequestContext(context.Background(), requestContext)
	return sharedtelemetry.WithActor(ctx, sharedtelemetry.MemberActor{MemberID: uuid.NewString()})
}

func recordingAccess(steps *[]string, err error) Access {
	return Access{check: func(context.Context, *gorm.DB, *auth.SpiceDBClient) error {
		*steps = append(*steps, "access")
		return err
	}}
}

func noteSpec(steps *[]string, text string) Spec[note] {
	return Spec[note]{
		Access: recordingAccess(steps, nil),
		Change: func(_ context.Context, tx *gorm.DB) (note, error) {
			*steps = append(*steps, "change")
			row := note{Text: text}
			return row, tx.Create(&row).Error
		},
		Audit: func(row note) Audit {
			*steps = append(*steps, "audit")
			return RequestAudit(sharedtelemetry.AuditMemberTagCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberTagCreatedAuditRecord(metadata, uuid.NewString(), row.Text)
			})
		},
		Outbox: func(context.Context, *gorm.DB, note) error {
			*steps = append(*steps, "outbox")
			return nil
		},
	}
}

func noteCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&note{}).Count(&count).Error)
	return count
}

func TestRunOrdersAccessChangeAuditOutboxInOneTransaction(t *testing.T) {
	recorder := &audittest.Recorder{}
	runner, db := newTestRunner(t, recorder)
	var steps []string

	result, err := Run(memberRequestContext(t), runner, noteSpec(&steps, "kept"))

	require.NoError(t, err)
	require.Equal(t, "kept", result.Text)
	require.Equal(t, []string{"access", "change", "audit", "outbox"}, steps)
	require.Len(t, recorder.Records(), 1)
	require.Equal(t, sharedtelemetry.AuditMemberTagCreated, recorder.Records()[0].Action)
	require.EqualValues(t, 1, noteCount(t, db))
}

func TestRunRollsBackWhenALaterStepFails(t *testing.T) {
	denied := errors.New("denied")
	auditDown := errors.New("audit unavailable")
	outboxDown := errors.New("queue unavailable")
	for _, tc := range []struct {
		name     string
		recorder *audittest.Recorder
		edit     func(*Spec[note], *[]string)
		want     error
		steps    []string
	}{
		{
			name: "access", recorder: &audittest.Recorder{},
			edit: func(spec *Spec[note], steps *[]string) { spec.Access = recordingAccess(steps, denied) },
			want: denied, steps: []string{"access"},
		},
		{
			name: "audit", recorder: &audittest.Recorder{Err: auditDown},
			edit: func(*Spec[note], *[]string) {}, want: auditDown, steps: []string{"access", "change", "audit"},
		},
		{
			name: "outbox", recorder: &audittest.Recorder{},
			edit: func(spec *Spec[note], steps *[]string) {
				spec.Outbox = func(context.Context, *gorm.DB, note) error {
					*steps = append(*steps, "outbox")
					return outboxDown
				}
			},
			want: outboxDown, steps: []string{"access", "change", "audit", "outbox"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, db := newTestRunner(t, tc.recorder)
			var steps []string
			spec := noteSpec(&steps, "rolled back")
			tc.edit(&spec, &steps)

			result, err := Run(memberRequestContext(t), runner, spec)

			require.ErrorIs(t, err, tc.want)
			require.Zero(t, result)
			require.Equal(t, tc.steps, steps)
			require.Zero(t, noteCount(t, db))
		})
	}
}

func TestRunRequiresAnExplicitAuditDecision(t *testing.T) {
	recorder := &audittest.Recorder{}
	runner, db := newTestRunner(t, recorder)
	var steps []string
	spec := noteSpec(&steps, "unaudited")
	spec.Audit = func(note) Audit { return Audit{} }

	_, err := Run(memberRequestContext(t), runner, spec)
	require.ErrorContains(t, err, "return Unchanged")
	require.Zero(t, noteCount(t, db))

	spec.Audit = func(note) Audit { return Unchanged() }
	_, err = Run(memberRequestContext(t), runner, spec)
	require.NoError(t, err)
	require.Empty(t, recorder.Records())
	require.EqualValues(t, 1, noteCount(t, db))
}

func TestAuditsAppendsEveryChangedRecord(t *testing.T) {
	recorder := &audittest.Recorder{}
	runner, _ := newTestRunner(t, recorder)
	var steps []string
	spec := noteSpec(&steps, "two facets")
	record := func(targetID string) Audit {
		return RequestAudit(sharedtelemetry.AuditMemberTagCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMemberTagCreatedAuditRecord(metadata, targetID, "tag")
		})
	}
	first, second := uuid.NewString(), uuid.NewString()
	spec.Audit = func(note) Audit { return Audits(record(first), Unchanged(), record(second)) }
	_, err := Run(memberRequestContext(t), runner, spec)
	require.NoError(t, err)
	records := recorder.Records()
	require.Len(t, records, 2)
	require.Equal(t, []string{first, second}, []string{records[0].TargetID, records[1].TargetID})

	spec.Audit = func(note) Audit { return Audits() }
	_, err = Run(memberRequestContext(t), runner, spec)
	require.NoError(t, err)
	require.Len(t, recorder.Records(), 2)

	spec.Audit = func(note) Audit { return Audits(Unchanged(), Unchanged()) }
	_, err = Run(memberRequestContext(t), runner, spec)
	require.NoError(t, err)
	require.Len(t, recorder.Records(), 2)

	spec.Audit = func(note) Audit { return Audits(Audit{}) }
	_, err = Run(memberRequestContext(t), runner, spec)
	require.ErrorContains(t, err, "return Unchanged")
}

func TestRunRejectsAnIncompleteSpecBeforeTheTransaction(t *testing.T) {
	runner, _ := newTestRunner(t, &audittest.Recorder{})
	var steps []string
	complete := noteSpec(&steps, "never")
	descriptorErr := errors.New("invalid resource id")
	for name, edit := range map[string]func(*Spec[note]){
		"access":     func(spec *Spec[note]) { spec.Access = Access{} },
		"change":     func(spec *Spec[note]) { spec.Change = nil },
		"audit":      func(spec *Spec[note]) { spec.Audit = nil },
		"subject":    func(spec *Spec[note]) { spec.Subjects = []string{" "} },
		"descriptor": func(spec *Spec[note]) { spec.Access = FreshAdminCan(policyv1.Can{}, descriptorErr) },
		"locked":     func(spec *Spec[note]) { spec.Access = Locked(nil) },
	} {
		t.Run(name, func(t *testing.T) {
			spec := complete
			edit(&spec)
			_, err := Run(memberRequestContext(t), runner, spec)
			require.Error(t, err)
			require.Empty(t, steps)
		})
	}
}

func TestLockedChecksTheTargetInTheChangeTransaction(t *testing.T) {
	runner, db := newTestRunner(t, &audittest.Recorder{})
	var steps []string
	spec := noteSpec(&steps, "after check")
	spec.Access = Locked(func(_ context.Context, tx *gorm.DB, _ Require) error {
		steps = append(steps, "access")
		return tx.Create(&note{Text: "locked target"}).Error
	})

	_, err := Run(memberRequestContext(t), runner, spec)
	require.NoError(t, err)
	require.Equal(t, []string{"access", "change", "audit", "outbox"}, steps)
	require.EqualValues(t, 2, noteCount(t, db))

	missing := errors.New("target not found")
	steps = nil
	spec.Access = Locked(func(_ context.Context, tx *gorm.DB, _ Require) error {
		steps = append(steps, "access")
		if err := tx.Create(&note{Text: "rolled back"}).Error; err != nil {
			return err
		}
		return missing
	})
	_, err = Run(memberRequestContext(t), runner, spec)
	require.ErrorIs(t, err, missing)
	require.Equal(t, []string{"access"}, steps)
	require.EqualValues(t, 2, noteCount(t, db))
}

func TestIsDeniedRecognizesFreshCheckDenials(t *testing.T) {
	require.True(t, IsDenied(fmt.Errorf("authorize: %w", errs.NoPermission("edit", "post"))))
	require.True(t, IsDenied(domainerr.Denied("not yours")))
	require.False(t, IsDenied(errs.NotFound("post", "id")))
	require.False(t, IsDenied(domainerr.ResourceNotFound("post", "id")))
	require.False(t, IsDenied(errors.New("database is down")))
	require.False(t, IsDenied(domainerr.ResourceNotFound("post", "id").WithCause(errs.NoPermission("edit", "post"))))
	require.True(t, IsUnauthenticated(fmt.Errorf("authorize: %w", errs.AuthenticationRequired())))
	require.True(t, IsUnauthenticated(domainerr.AuthenticationRequired()))
	require.False(t, IsUnauthenticated(errs.NoPermission("edit", "post")))
	require.True(t, IsNotFound(fmt.Errorf("lock: %w", errs.NotFound("member", "id"))))
	require.False(t, IsNotFound(errs.NoPermission("edit", "post")))
}

func TestNewRunnerRequiresEveryCollaborator(t *testing.T) {
	db := &gorm.DB{}
	_, err := NewRunner(nil, &auth.SpiceDBClient{}, &audittest.Recorder{})
	require.ErrorContains(t, err, "database")
	_, err = NewRunner(db, nil, &audittest.Recorder{})
	require.ErrorContains(t, err, "SpiceDB")
	_, err = NewRunner(db, &auth.SpiceDBClient{}, nil)
	require.ErrorContains(t, err, "audit")
}

func TestLockOrderSortsAndDeduplicatesSubjects(t *testing.T) {
	ordered, err := lockOrder([]string{"b", " a ", "b"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, ordered)
}

func TestCheckedRunsTheCheckInsideTheTransaction(t *testing.T) {
	runner, db := newTestRunner(t, &audittest.Recorder{})
	denied := errors.New("contributor revoked")
	var steps []string
	spec := noteSpec(&steps, "contributed")
	spec.Access = Checked(func(_ context.Context, tx *gorm.DB) error {
		steps = append(steps, "check")
		require.NoError(t, tx.Create(&note{Text: "inside"}).Error)
		return denied
	})

	_, err := Run(memberRequestContext(t), runner, spec)

	require.ErrorIs(t, err, denied)
	require.Equal(t, []string{"check"}, steps)
	require.Zero(t, noteCount(t, db))
	_, err = Run(memberRequestContext(t), runner, Spec[note]{Access: Checked(nil), Change: spec.Change, Audit: spec.Audit})
	require.ErrorContains(t, err, "access check is required")
}

func TestReadSeesOneSnapshot(t *testing.T) {
	_, db := newTestRunner(t, &audittest.Recorder{})
	require.NoError(t, db.Create(&note{Text: "stored"}).Error)
	var read []note
	require.NoError(t, Read(context.Background(), db, func(tx *gorm.DB) error { return tx.Find(&read).Error }))
	require.Len(t, read, 1)
	readErr := errors.New("read failed")
	require.ErrorIs(t, Read(context.Background(), db, func(*gorm.DB) error { return readErr }), readErr)
}

func TestRunTakesDomainLocksBeforeAccess(t *testing.T) {
	runner, db := newTestRunner(t, &audittest.Recorder{})
	var steps []string
	spec := noteSpec(&steps, "locked first")
	spec.Lock = func(context.Context, *gorm.DB) error {
		steps = append(steps, "lock")
		return nil
	}

	_, err := Run(memberRequestContext(t), runner, spec)
	require.NoError(t, err)
	require.Equal(t, []string{"lock", "access", "change", "audit", "outbox"}, steps)

	missing := errors.New("root not found")
	steps = nil
	spec.Lock = func(context.Context, *gorm.DB) error {
		steps = append(steps, "lock")
		return missing
	}
	_, err = Run(memberRequestContext(t), runner, spec)
	require.ErrorIs(t, err, missing)
	require.Equal(t, []string{"lock"}, steps)
	require.EqualValues(t, 1, noteCount(t, db))
}

func TestIsAnsweredRecognizesClassifiedFailures(t *testing.T) {
	require.True(t, IsAnswered(connect.NewError(connect.CodeNotFound, errors.New("missing"))))
	require.True(t, IsAnswered(fmt.Errorf("save: %w", domainerr.Precondition("kept"))))
	require.False(t, IsAnswered(errors.New("database unavailable")))
}
