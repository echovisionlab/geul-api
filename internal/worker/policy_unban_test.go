package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// identityPages is the Kratos identity directory, one slice per page.
type identityPages struct {
	pages     [][]*auth.Identity
	err       error
	listPages *[]int
}

func (p identityPages) ListIdentities(_ context.Context, page, perPage int) ([]*auth.Identity, int64, error) {
	if p.listPages != nil {
		*p.listPages = append(*p.listPages, page)
	}
	if p.err != nil {
		return nil, 0, p.err
	}
	if perPage != 100 {
		return nil, 0, fmt.Errorf("unexpected page size %d", perPage)
	}
	if page < 0 || page >= len(p.pages) {
		return []*auth.Identity{}, 0, nil
	}
	return p.pages[page], 0, nil
}

// clearedBans records the identities whose timed ban the worker cleared.
type clearedBans struct {
	cleared []string
	failing map[string]error
	stale   map[string]bool
}

func (b *clearedBans) ClearExpiredTimedBan(_ context.Context, identityID string, _ time.Time) (bool, error) {
	if err := b.failing[identityID]; err != nil {
		return false, err
	}
	b.cleared = append(b.cleared, identityID)
	return !b.stale[identityID], nil
}

func unbanIdentity(id string, metadataAdmin structured.Fields) *auth.Identity {
	return &auth.Identity{ID: id, MetadataAdmin: metadataAdmin}
}

func TestUnbanClearsOnlyExpiredTimedBans(t *testing.T) {
	now := time.Now().UTC()
	bans := &clearedBans{}
	handlers := newTestHandlers(t, Deps{TimedBans: bans, Identities: identityPages{pages: [][]*auth.Identity{{
		unbanIdentity("expired", structured.Fields{"banned": true, "ban_reason": "spam", "ban_expires": now.Add(-time.Minute).Format(time.RFC3339)}),
		unbanIdentity("expired-now", structured.Fields{"banned": true, "ban_expires": now.Format(time.RFC3339)}),
		unbanIdentity("future", structured.Fields{"banned": true, "ban_expires": now.Add(time.Hour).Format(time.RFC3339)}),
		unbanIdentity("permanent", structured.Fields{"banned": true, "ban_reason": "abuse"}),
		unbanIdentity("empty-expiry", structured.Fields{"banned": true, "ban_expires": ""}),
		unbanIdentity("invalid-expiry", structured.Fields{"banned": true, "ban_expires": "not-rfc3339"}),
		unbanIdentity("no-metadata", nil),
		unbanIdentity("cleared-ban", structured.Fields{"banned": false, "ban_expires": now.Add(-time.Hour).Format(time.RFC3339)}),
		unbanIdentity("non-bool-ban", structured.Fields{"banned": "true", "ban_expires": now.Add(-time.Hour).Format(time.RFC3339)}),
	}}}})

	require.NoError(t, handlers.handleUnbanExpired(t.Context(), now))

	require.Equal(t, []string{"expired", "expired-now"}, bans.cleared)
}

func TestUnbanPagesThroughEveryIdentity(t *testing.T) {
	now := time.Now().UTC()
	firstPage := make([]*auth.Identity, 100)
	for i := range firstPage {
		firstPage[i] = unbanIdentity(fmt.Sprintf("active-%03d", i), structured.Fields{"banned": false})
	}
	firstPage[17] = unbanIdentity("first-expired", structured.Fields{"banned": true, "ban_expires": now.Add(-2 * time.Minute).Format(time.RFC3339)})
	secondExpired := unbanIdentity("second-expired", structured.Fields{"banned": true, "ban_expires": now.Add(-time.Minute).Format(time.RFC3339)})
	var listed []int
	bans := &clearedBans{}
	handlers := newTestHandlers(t, Deps{TimedBans: bans, Identities: identityPages{
		pages: [][]*auth.Identity{firstPage, {secondExpired}}, listPages: &listed,
	}})

	require.NoError(t, handlers.handleUnbanExpired(t.Context(), now))

	require.Equal(t, []int{0, 1}, listed, "a short page ends the scan")
	require.Equal(t, []string{"first-expired", "second-expired"}, bans.cleared)
}

func TestUnbanGoesOnPastAFailedClear(t *testing.T) {
	now := time.Now().UTC()
	expired := structured.Fields{"banned": true, "ban_expires": now.Add(-time.Minute).Format(time.RFC3339)}
	bans := &clearedBans{failing: map[string]error{"failing": errors.New("fence busy")}, stale: map[string]bool{"stale": true}}
	handlers := newTestHandlers(t, Deps{TimedBans: bans, Identities: identityPages{pages: [][]*auth.Identity{{
		unbanIdentity("failing", expired), unbanIdentity("stale", expired), unbanIdentity("cleared", expired),
	}}}})

	require.NoError(t, handlers.handleUnbanExpired(t.Context(), now))
	require.Equal(t, []string{"stale", "cleared"}, bans.cleared)
}

func TestUnbanFailsWhenTheIdentitiesCannotBeListed(t *testing.T) {
	bans := &clearedBans{}
	handlers := newTestHandlers(t, Deps{TimedBans: bans, Identities: identityPages{err: errors.New("kratos unavailable")}})

	err := handlers.handleUnbanExpired(t.Context(), time.Now())

	require.ErrorContains(t, err, "failed to list identities")
	require.Empty(t, bans.cleared)
}

// legalVersions plays the Legal port outside the activation transaction,
// recording the job transaction's steps each dispatch saw.
type legalVersions struct {
	unreachable
	scheduled  []string
	err        error
	dispatched []string
	notices    []EffectiveNotice
	units      *mutationtest.UnitOfWork[Jobs]
	seen       [][]mutation.Step
}

func (l *legalVersions) ScheduledVersions(context.Context, LegalKind) ([]string, error) {
	return l.scheduled, l.err
}

func (l *legalVersions) DispatchEffectiveNotice(_ context.Context, kind LegalKind, documentID string, _ time.Time, notice EffectiveNotice) {
	l.dispatched = append(l.dispatched, string(kind)+" "+documentID)
	l.notices = append(l.notices, notice)
	l.seen = append(l.seen, l.units.Steps())
}

func newLegalHandlers(t *testing.T, legal *legalVersions, jobs *fakeJobs) (*Handlers, *mutationtest.UnitOfWork[Jobs]) {
	t.Helper()
	legal.units = mutationtest.New[Jobs](jobs)
	return newTestHandlers(t, Deps{Units: legal.units, Legal: legal, SiteOrigin: "https://www.example.test"}), legal.units
}

func TestLegalActivationActivatesTheScheduledVersionAndDispatchesAfterCommit(t *testing.T) {
	for _, tt := range []struct {
		job   legalActivation
		kind  string
		event string
	}{
		{job: termsActivation, kind: "terms", event: "terms_effective"},
		{job: privacyActivation, kind: "privacy", event: "privacy_effective"},
	} {
		t.Run(tt.kind, func(t *testing.T) {
			jobs := newFakeJobs()
			jobs.sealed = "sealed run"
			legal := &legalVersions{unreachable: unreachable{t: t}, scheduled: []string{"version-2"}}
			handlers, units := newLegalHandlers(t, legal, jobs)

			require.NoError(t, handlers.handleActivateLegal(t.Context(), tt.job))

			require.Equal(t, []string{
				"ActivateDue " + tt.kind + " version-2",
				"RequestOG " + tt.kind + " version-2 " + tt.kind + "_scheduled_activated",
			}, jobs.calls.all())
			require.Equal(t, []string{tt.event}, jobs.eventKeys)
			require.Equal(t, []map[string]string{{tt.kind + "_url": "https://www.example.test/" + tt.kind}}, jobs.templates)
			require.Equal(t, []string{tt.kind + " version-2"}, legal.dispatched)
			require.Equal(t, []EffectiveNotice{"sealed run"}, legal.notices, "the sealed run is the one dispatched")
			require.Equal(t, [][]mutation.Step{{mutation.StepCommit}}, legal.seen, "the notice is dispatched after commit")
			require.Equal(t, []mutation.Step{mutation.StepCommit}, units.Steps())
		})
	}
}

func TestLegalActivationWithoutAScheduledVersionOpensNoTransaction(t *testing.T) {
	jobs := newFakeJobs()
	legal := &legalVersions{unreachable: unreachable{t: t}}
	handlers, units := newLegalHandlers(t, legal, jobs)

	require.NoError(t, handlers.handleActivateLegal(t.Context(), termsActivation))
	require.Empty(t, units.Steps())
	require.Empty(t, jobs.calls.all())
}

func TestLegalActivationFailsClosedOnSeveralScheduledVersions(t *testing.T) {
	for _, job := range []legalActivation{termsActivation, privacyActivation} {
		jobs := newFakeJobs()
		legal := &legalVersions{unreachable: unreachable{t: t}, scheduled: []string{"version-2", "version-3"}}
		handlers, units := newLegalHandlers(t, legal, jobs)

		err := handlers.handleActivateLegal(t.Context(), job)

		require.ErrorContains(t, err, fmt.Sprintf("multiple scheduled %s versions violate the activation invariant", job.kind))
		require.Empty(t, units.Steps(), "nothing changes")
	}
}

func TestLegalActivationOfAVersionNotDueChangesNothing(t *testing.T) {
	jobs := newFakeJobs()
	jobs.due = false
	legal := &legalVersions{unreachable: unreachable{t: t}, scheduled: []string{"version-2"}}
	handlers, _ := newLegalHandlers(t, legal, jobs)

	require.NoError(t, handlers.handleActivateLegal(t.Context(), privacyActivation))
	require.Equal(t, []string{"ActivateDue privacy version-2"}, jobs.calls.all(), "no OG target switch")
	require.Empty(t, legal.dispatched)
}

func TestLegalActivationRollsBackWhenAStepFails(t *testing.T) {
	failed := errors.New("step failed")
	for name, fail := range map[string]func(*fakeJobs){
		"activation": func(jobs *fakeJobs) { jobs.activate = failed },
		"OG target":  func(jobs *fakeJobs) { jobs.requestOG = failed },
	} {
		t.Run(name, func(t *testing.T) {
			jobs := newFakeJobs()
			fail(jobs)
			legal := &legalVersions{unreachable: unreachable{t: t}, scheduled: []string{"version-2"}}
			handlers, units := newLegalHandlers(t, legal, jobs)

			require.ErrorIs(t, handlers.handleActivateLegal(t.Context(), termsActivation), failed)

			require.Empty(t, jobs.activations, "the activation rolls back with the OG target")
			require.Empty(t, legal.dispatched, "nothing is dispatched")
			require.Equal(t, []mutation.Step{mutation.StepRollback}, units.Steps())
		})
	}
}

func TestLegalActivationFailsWhenTheScheduledVersionsCannotBeRead(t *testing.T) {
	failed := errors.New("read failed")
	legal := &legalVersions{unreachable: unreachable{t: t}, err: failed}
	handlers, units := newLegalHandlers(t, legal, newFakeJobs())

	require.ErrorIs(t, handlers.handleActivateLegal(t.Context(), termsActivation), failed)
	require.Empty(t, units.Steps())
}
