package series_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/series"
	"github.com/echovisionlab/geul-api/internal/series/seriestest"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// fixture is one Series service over the fakes and the principal its
// requests carry.
type fixture struct {
	store   *seriestest.Store
	units   *mutationtest.UnitOfWork[series.Store]
	checker *authztest.Checker
	spiceDB *seriestest.Authorizer
	fence   *identitystatetest.Fence
	audit   *audittest.Recorder
	media   *fakeMedia
	members fakeMembers
	locales *fakeLocales
	service *series.Service

	identityID string
	memberID   string
}

// newFixture serves the Series use cases to a principal the checker allows
// everything a test does not deny.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, units := seriestest.New()
	f := &fixture{
		store: store, units: units, checker: authztest.AllowAll(), fence: &identitystatetest.Fence{},
		audit: &audittest.Recorder{}, media: &fakeMedia{assets: map[string]*series.Asset{}, images: map[string]*series.Asset{}},
		members: fakeMembers{}, locales: &fakeLocales{defaultLocale: "en"},
		identityID: uuid.NewString(), memberID: uuid.NewString(),
	}
	f.spiceDB = seriestest.NewAuthorizer(f.checker)
	service, err := series.NewService(series.Deps{
		Units: units, SpiceDB: f.spiceDB, Fence: f.fence, Audit: f.audit,
		Media: f.media, Reads: fakeReads{}, Members: f.members, Locales: f.locales,
	})
	require.NoError(t, err)
	f.service = service
	return f
}

// ctx is a member request of the principal, so mutations name their audit
// records.
func (f *fixture) ctx(t *testing.T) context.Context {
	t.Helper()
	return principalContext(t, f.identityID, f.memberID)
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

// seedSeries stores a Series with a source copy titled title.
func (f *fixture) seedSeries(title string) series.Series {
	return f.store.SeedSeries(series.Series{Title: title})
}

// lastStep is the end of the last transaction the unit of work ran.
func (f *fixture) lastStep() mutation.Step {
	steps := f.units.Steps()
	if len(steps) == 0 {
		return ""
	}
	return steps[len(steps)-1]
}

func (f *fixture) actions() []sharedtelemetry.AuditAction {
	var actions []sharedtelemetry.AuditAction
	for _, record := range f.audit.Records() {
		actions = append(actions, record.Action)
	}
	return actions
}

func codeOf(err error) connect.Code {
	return errs.Wrap(err).Code()
}

func requireCode(t *testing.T, want connect.Code, err error) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, want, codeOf(err), "%v", err)
}

// must returns a generated descriptor's value, failing on its error.
func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func text(value string) *string { return &value }

var errDown = errors.New("collaborator unavailable")

// fakeMedia projects the ready assets a test set.
type fakeMedia struct {
	assets map[string]*series.Asset // by asset ID
	images map[string]*series.Asset // by source File ID
	err    error
}

func (m *fakeMedia) ReadyAsset(_ context.Context, candidateIDs ...*string) (*series.Asset, error) {
	for _, id := range candidateIDs {
		if id != nil && m.assets[*id] != nil {
			return m.assets[*id], nil
		}
	}
	return nil, nil
}

func (m *fakeMedia) ReadyAssets(_ context.Context, candidateIDs ...*string) (map[string]*series.Asset, error) {
	ready := map[string]*series.Asset{}
	for _, id := range candidateIDs {
		if id != nil && m.assets[*id] != nil {
			ready[*id] = m.assets[*id]
		}
	}
	return ready, nil
}

func (m *fakeMedia) ReadyAssetsForSourceFiles(_ context.Context, _ string, fileIDs []string) (map[string]*series.Asset, error) {
	ready := map[string]*series.Asset{}
	for _, id := range fileIDs {
		if m.images[id] != nil {
			ready[id] = m.images[id]
		}
	}
	return ready, nil
}

func (m *fakeMedia) ReadyAssetForSourceFile(_ context.Context, fileID, _ string) (*series.Asset, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.images[fileID], nil
}

// fakeReads counts nothing.
type fakeReads struct{}

func (fakeReads) LoadPostCounts(context.Context, []string) (map[string]int32, error) {
	return map[string]int32{}, nil
}

func (fakeReads) LoadManagerCounts(context.Context, []string) (map[string]int32, error) {
	return map[string]int32{}, nil
}

// fakeMembers is the Member summaries by Member ID.
type fakeMembers map[string]series.ManagerSummary

func (m fakeMembers) LoadSeriesManagers(_ context.Context, memberIDs []string) (map[string]series.ManagerSummary, error) {
	found := map[string]series.ManagerSummary{}
	for _, id := range memberIDs {
		if summary, ok := m[id]; ok {
			found[id] = summary
		}
	}
	return found, nil
}

type fakeLocales struct {
	preferred     *string
	defaultLocale string
	err           error
}

func (l *fakeLocales) MemberPreferredLocale(context.Context, string) (*string, error) {
	return l.preferred, l.err
}

func (l *fakeLocales) DefaultTranslationLocale(context.Context) (string, error) {
	return l.defaultLocale, l.err
}

var contractTime = time.Date(2026, 8, 23, 4, 0, 0, 0, time.UTC)
