package legal_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/legal/legaltest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// lookupStore records the optional lookups and their transaction scope.
type lookupStore struct {
	legal.Store
	inTransaction *atomic.Bool
	insideReads   *atomic.Int32
	totalReads    *atomic.Int32
}

func (s lookupStore) Savepoint() func() {
	return s.Store.(*legaltest.Store).Savepoint()
}

func (s lookupStore) PreferredLocale(ctx context.Context, memberID string) (*string, error) {
	s.totalReads.Add(1)
	if s.inTransaction.Load() {
		s.insideReads.Add(1)
	}
	return s.Store.PreferredLocale(ctx, memberID)
}

func (s lookupStore) TranslationSettings(ctx context.Context) (translation.RuntimeSettings, error) {
	s.totalReads.Add(1)
	if s.inTransaction.Load() {
		s.insideReads.Add(1)
	}
	return s.Store.TranslationSettings(ctx)
}

// transactionMarker marks the transactions of a unit of work.
type transactionMarker struct {
	*mutationtest.UnitOfWork[legal.Store]
	inTransaction *atomic.Bool
}

func (u transactionMarker) Transaction(ctx context.Context, lockAuthorization bool, work func(mutation.Tx[legal.Store]) error) error {
	u.inTransaction.Store(true)
	defer u.inTransaction.Store(false)
	return u.UnitOfWork.Transaction(ctx, lockAuthorization, work)
}

func (u transactionMarker) RelationshipTransaction(
	ctx context.Context,
	writer authzmutation.RelationshipWriter,
	work func(mutation.Tx[legal.Store], authzmutation.WriteRelationships) error,
) error {
	u.inTransaction.Store(true)
	defer u.inTransaction.Store(false)
	return u.UnitOfWork.RelationshipTransaction(ctx, writer, work)
}

func TestCreatePolicyVersionAuthorizesBeforeSourceLocaleLookups(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "allowed"}[allowed], func(t *testing.T) {
			store := legaltest.NewStore()
			inTransaction, insideReads, totalReads := &atomic.Bool{}, &atomic.Int32{}, &atomic.Int32{}
			units := mutationtest.New[legal.Store](lookupStore{Store: store, inTransaction: inTransaction, insideReads: insideReads, totalReads: totalReads})
			checker := &authztest.Checker{}
			if allowed {
				checker = authztest.AllowAll()
			}
			service, err := legal.NewService(legal.Deps{Units: transactionMarker{UnitOfWork: units, inTransaction: inTransaction}, SpiceDB: checker, Fence: &identitystatetest.Fence{}, Audit: &audittest.Recorder{}, Dispatch: &dispatch{}, OGAssets: &ogAssets{}})
			require.NoError(t, err)
			created, err := service.CreatePolicyVersion(newPrincipal().context(t), legal.Terms, legal.CreatePolicyVersionCommand{})
			require.NotEmpty(t, checker.Checks())
			if !allowed {
				require.Error(t, err)
				require.Zero(t, totalReads.Load(), "a denied caller must not query locale preferences or initialize settings")
				return
			}
			require.NoError(t, err)
			require.Equal(t, "en", created.SourceLocale)
			require.EqualValues(t, 2, totalReads.Load())
			require.EqualValues(t, 2, insideReads.Load(), "optional source lookups belong to the authorized creation transaction")
		})
	}
}
