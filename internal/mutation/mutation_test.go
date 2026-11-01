package mutation

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
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
	return newTestRunnerWith(t, authztest.AllowAll(), &identitystatetest.Fence{}, recorder)
}

func newTestRunnerWith(t *testing.T, checker *authztest.Checker, fence *identitystatetest.Fence, recorder *audittest.Recorder) (*Runner, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&note{}))
	runner, err := NewRunner(db, checker, fence, recorder)
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
	return Access{check: func(context.Context, *gorm.DB, any, authority) error {
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

func TestVersionAuditAttributesTheRequestMemberOrSystem(t *testing.T) {
	recorder := &audittest.Recorder{}
	runner, _ := newTestRunner(t, recorder)
	var steps []string
	spec := noteSpec(&steps, "checkpoint")
	spec.Audit = func(note) Audit {
		return VersionAudit(sharedtelemetry.AuditMemberTagCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMemberTagCreatedAuditRecord(metadata, uuid.NewString(), "tag")
		})
	}
	_, err := Run(memberRequestContext(t), runner, spec)
	require.NoError(t, err)
	require.Len(t, recorder.Records(), 1)

	_, err = Run(context.Background(), runner, spec)
	require.ErrorContains(t, err, "version domain audit requires request context")
	require.Len(t, recorder.Records(), 1)
}

func TestAnonymousAuditAttributesAVisitorWithoutASession(t *testing.T) {
	recorder := &audittest.Recorder{}
	runner, _ := newTestRunner(t, recorder)
	var steps []string
	spec := noteSpec(&steps, "submission")
	spec.Audit = func(note) Audit {
		return AnonymousAudit(sharedtelemetry.AuditFormSubmissionCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewFormSubmissionCreatedAuditRecord(metadata, uuid.NewString(), uuid.NewString())
		})
	}
	_, err := Run(context.Background(), runner, spec)
	require.NoError(t, err)
	records := recorder.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.ActorKindAnonymous, records[0].Kind)
	require.Empty(t, records[0].MemberID)
}

func TestSystemAuditAttributesTheServiceWhateverTheRequestActor(t *testing.T) {
	recorder := &audittest.Recorder{}
	runner, _ := newTestRunner(t, recorder)
	var steps []string
	spec := noteSpec(&steps, "role")
	spec.Audit = func(note) Audit {
		return SystemAudit(sharedtelemetry.ServiceBackend, sharedtelemetry.AuditMemberUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMemberRoleUpdatedAuditRecord(metadata, uuid.NewString(), "user", "admin")
		})
	}
	for _, ctx := range []context.Context{context.Background(), memberRequestContext(t)} {
		_, err := Run(ctx, runner, spec)
		require.NoError(t, err)
	}
	records := recorder.Records()
	require.Len(t, records, 2)
	for _, record := range records {
		require.Equal(t, sharedtelemetry.ActorKindSystem, record.Kind)
		require.Equal(t, string(sharedtelemetry.ServiceBackend), record.Service)
		require.Empty(t, record.MemberID)
	}
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
		"locked":     func(spec *Spec[note]) { spec.Access = Locked[*gorm.DB](nil) },
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
	require.True(t, IsClassified(fmt.Errorf("lock: %w", errs.NotFound("member", "id"))))
	require.True(t, IsClassified(domainerr.Precondition("changed")))
	require.False(t, IsClassified(errors.New("database is down")))
}

func TestNewRunnerRequiresEveryCollaborator(t *testing.T) {
	db := &gorm.DB{}
	checker, fence := authztest.AllowAll(), &identitystatetest.Fence{}
	_, err := NewRunner(nil, checker, fence, &audittest.Recorder{})
	require.ErrorContains(t, err, "database")
	_, err = NewRunner(db, nil, fence, &audittest.Recorder{})
	require.ErrorContains(t, err, "SpiceDB")
	_, err = NewRunner(db, checker, nil, &audittest.Recorder{})
	require.ErrorContains(t, err, "identity fence")
	_, err = NewRunner(db, checker, fence, nil)
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
	_, err = Run(memberRequestContext(t), runner, Spec[note]{Access: Checked[*gorm.DB](nil), Change: spec.Change, Audit: spec.Audit})
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

func principalContext(t *testing.T) (context.Context, *auth.UserInfo) {
	t.Helper()
	principal := &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	}
	return auth.WithUser(memberRequestContext(t), principal), principal
}

func TestFreshAccessAsksTheFenceThenTheAuthorizer(t *testing.T) {
	can, err := policyv1.Platform.IsAdmin()
	require.NoError(t, err)
	for _, tc := range []struct {
		name     string
		checker  func() *authztest.Checker
		inactive bool
		access   func() Access
		want     connect.Code
		checked  bool
	}{
		{name: "allowed", checker: authztest.AllowAll, access: func() Access { return FreshCan(can, nil) }, checked: true},
		{
			name: "denied", checker: func() *authztest.Checker { return &authztest.Checker{} },
			access: func() Access { return FreshCan(can, nil) }, want: connect.CodePermissionDenied, checked: true,
		},
		{
			name: "admin denied", checker: func() *authztest.Checker { return &authztest.Checker{} },
			access: func() Access { return FreshAdminCan(can, nil) }, want: connect.CodePermissionDenied, checked: true,
		},
		{
			name: "inactive principal", checker: authztest.AllowAll, inactive: true,
			access: func() Access { return FreshCan(can, nil) }, want: connect.CodePermissionDenied,
		},
		{
			name: "locked", checker: authztest.AllowAll, checked: true,
			access: func() Access {
				return Locked(func(_ context.Context, _ *gorm.DB, require Require) error { return require(can) })
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, principal := principalContext(t)
			checker, fence := tc.checker(), &identitystatetest.Fence{}
			if tc.inactive {
				fence.Deactivate(principal.IdentityID.String())
			}
			runner, db := newTestRunnerWith(t, checker, fence, &audittest.Recorder{})
			var steps []string
			spec := noteSpec(&steps, "fresh")
			spec.Access = tc.access()

			_, err := Run(ctx, runner, spec)

			if tc.want == 0 {
				require.NoError(t, err)
				require.EqualValues(t, 1, noteCount(t, db))
			} else {
				require.Equal(t, tc.want, errs.Wrap(err).Code())
				require.Zero(t, noteCount(t, db))
			}
			require.Equal(t, []string{principal.IdentityID.String()}, fence.Locked())
			if tc.checked {
				require.Equal(t, []authztest.Check{authztest.Of(principal.IdentityID.String(), can, nil)}, checker.Checks())
			} else {
				require.Empty(t, checker.Checks())
			}
		})
	}
}

func TestRunLocksSubjectsThroughTheFenceAndWritesRelationshipsThroughTheAuthorizer(t *testing.T) {
	checker, fence := authztest.AllowAll(), &identitystatetest.Fence{}
	runner, db := newTestRunnerWith(t, checker, fence, &audittest.Recorder{})
	ctx, principal := principalContext(t)
	can, err := policyv1.Platform.IsAdmin()
	require.NoError(t, err)
	touch, err := policyv1.SiteSetting.TouchPolicy("1")
	require.NoError(t, err)
	remove, err := policyv1.SiteSetting.DeletePolicy("1")
	require.NoError(t, err)
	var steps []string
	spec := noteSpec(&steps, "with relationships")
	spec.Access = FreshAdminCan(can, nil)
	spec.Subjects = []string{"b", "a"}
	spec.Relationships = func(note) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
		return []policyv1.RelationshipMutation{touch}, []policyv1.RelationshipMutation{remove}, nil
	}

	_, err = Run(ctx, runner, spec)

	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", principal.IdentityID.String()}, fence.Locked())
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, checker.Applied())
	require.Empty(t, checker.Compensated())
	require.EqualValues(t, 1, noteCount(t, db))

	steps = nil
	outboxDown := errors.New("queue unavailable")
	spec.Outbox = func(context.Context, *gorm.DB, note) error { return outboxDown }
	_, err = Run(ctx, runner, spec)
	require.ErrorIs(t, err, outboxDown)
	require.Len(t, checker.Applied(), 1, "a failure before the write applies nothing")
	require.EqualValues(t, 1, noteCount(t, db))
}

func TestFencedLocksThePrincipalThroughTheFenceForTheDomainCheck(t *testing.T) {
	for _, inactive := range []bool{false, true} {
		t.Run(fmt.Sprintf("inactive=%v", inactive), func(t *testing.T) {
			ctx, principal := principalContext(t)
			fence := &identitystatetest.Fence{}
			if inactive {
				fence.Deactivate(principal.IdentityID.String())
			}
			runner, db := newTestRunnerWith(t, authztest.AllowAll(), fence, &audittest.Recorder{})
			hidden := errors.New("hidden")
			var steps []string
			spec := noteSpec(&steps, "fenced")
			spec.Access = Fenced(func(ctx context.Context, _ *gorm.DB, lockActive func(*auth.UserInfo) (bool, error)) error {
				active, err := lockActive(auth.GetUser(ctx))
				require.NoError(t, err)
				if !active {
					return hidden
				}
				return nil
			})

			_, err := Run(ctx, runner, spec)

			require.Equal(t, []string{principal.IdentityID.String()}, fence.Locked())
			if inactive {
				require.ErrorIs(t, err, hidden)
				require.Zero(t, noteCount(t, db))
				return
			}
			require.NoError(t, err)
			require.EqualValues(t, 1, noteCount(t, db))
		})
	}
	_, err := Run(memberRequestContext(t), mustRunner(t), Spec[note]{
		Access: Fenced[*gorm.DB](nil), Change: func(context.Context, *gorm.DB) (note, error) { return note{}, nil },
		Audit: func(note) Audit { return Unchanged() },
	})
	require.ErrorContains(t, err, "fenced access check is required")
}

func mustRunner(t *testing.T) *Runner {
	t.Helper()
	runner, _ := newTestRunner(t, &audittest.Recorder{})
	return runner
}
