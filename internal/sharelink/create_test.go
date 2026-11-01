package sharelink_test

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// targetAudit is the record a link change appends to its target's audit.
type targetAudit struct {
	action     sharedtelemetry.AuditAction
	targetType string
	scope      sharedtelemetry.AuditItemScope
	policy     sharedtelemetry.AuditPolicyType
}

var targetCases = map[sharelink.EntityType]struct {
	manage auth.ResourceAction
	state  func() sharelink.Target
	audit  targetAudit
}{
	sharelink.EntityPost:          {manage: policyv1.Post.ManageShareLinks, audit: targetAudit{action: sharedtelemetry.AuditPostUpdated, targetType: "post"}},
	sharelink.EntityPage:          {manage: policyv1.Page.ManageShareLinks, audit: targetAudit{action: sharedtelemetry.AuditPageUpdated, targetType: "page"}},
	sharelink.EntityWork:          {manage: policyv1.Work.ManageShareLinks, audit: targetAudit{action: sharedtelemetry.AuditWorkUpdated, targetType: "work"}},
	sharelink.EntityRelease:       {manage: policyv1.Release.ManageShareLinks, audit: targetAudit{action: sharedtelemetry.AuditReleaseUpdated, targetType: "release"}},
	sharelink.EntityForm:          {manage: policyv1.Form.ManageShareLinks, audit: targetAudit{action: sharedtelemetry.AuditFormUpdated, targetType: "form", scope: sharedtelemetry.AuditItemScopeForm}},
	sharelink.EntityFormDashboard: {manage: policyv1.Form.ManageShareLinks, audit: targetAudit{action: sharedtelemetry.AuditFormUpdated, targetType: "form", scope: sharedtelemetry.AuditItemScopeDashboard}},
	sharelink.EntityArtist:        {manage: policyv1.Artist.ManageShareLinks, audit: targetAudit{action: sharedtelemetry.AuditArtistUpdated, targetType: "artist"}},
	sharelink.EntityLabel:         {manage: policyv1.Label.ManageShareLinks, audit: targetAudit{action: sharedtelemetry.AuditLabelUpdated, targetType: "label"}},
	sharelink.EntityPrivacy: {
		manage: policyv1.PrivacyHistory.ManageShareLinks, state: scheduledVersion,
		audit: targetAudit{action: sharedtelemetry.AuditLegalPolicyUpdated, targetType: "legal_policy", policy: sharedtelemetry.AuditPolicyTypePrivacy},
	},
	sharelink.EntityTerms: {
		manage: policyv1.TermsHistory.ManageShareLinks, state: scheduledVersion,
		audit: targetAudit{action: sharedtelemetry.AuditLegalPolicyUpdated, targetType: "legal_policy", policy: sharedtelemetry.AuditPolicyTypeTerms},
	},
}

func scheduledVersion() sharelink.Target {
	effective := time.Now().Add(time.Hour)
	return sharelink.Target{Scheduled: true, EffectiveFrom: &effective, Version: 7}
}

// requireLinkAudit asserts the one record of a link change: it names the
// link's own target, never another kind's.
func requireLinkAudit(t *testing.T, want targetAudit, targetID, linkID string, operation sharedtelemetry.AuditItemOperation, record sharedtelemetry.AuditRecord) {
	t.Helper()
	require.Equal(t, want.action, record.Action)
	require.Equal(t, want.targetType, record.TargetType)
	require.Equal(t, targetID, record.TargetID)
	require.Equal(t, linkID, record.ItemID)
	require.Equal(t, operation, record.ItemOperation)
	require.Equal(t, want.scope, record.ItemScope)
	require.Equal(t, []string{"share_links"}, record.ChangedFields)
	require.Equal(t, want.policy, record.PolicyType)
	if want.policy != "" {
		require.Equal(t, int64(7), *record.VersionNumber)
	}
	require.Equal(t, callerMemberID, record.RecordActor.MemberID)
}

func TestCreateAndDeleteShareLinkAuthorizeAndAuditEveryTargetKind(t *testing.T) {
	t.Parallel()
	for entityType, tc := range targetCases {
		t.Run(string(entityType), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			state := sharelink.Target{}
			if tc.state != nil {
				state = tc.state()
			}
			id := f.target(entityType, state)
			f.checker.Allow(tc.manage(id))

			created, err := f.links.CreateShareLink(callerContext(t), sharelink.CreateShareLinkCommand{EntityType: entityType, EntityID: id})
			require.NoError(t, err)
			require.Equal(t, []sharelink.Link{created}, f.store.Links())
			require.NoError(t, f.links.DeleteShareLink(callerContext(t), sharelink.DeleteShareLinkCommand{ID: created.ID}))
			require.Empty(t, f.store.Links())

			require.Equal(t, []authztest.Check{check(tc.manage, id), check(tc.manage, id)}, f.checker.Checks())
			require.Equal(t, []string{callerIdentityID, callerIdentityID}, f.fence.Locked())
			require.Equal(t, append(append([]mutation.Step{}, committed...), committed...), f.units.Steps())
			records := f.audit.Records()
			require.Len(t, records, 2)
			requireLinkAudit(t, tc.audit, id, created.ID, sharedtelemetry.AuditItemOperationCreated, records[0])
			requireLinkAudit(t, tc.audit, id, created.ID, sharedtelemetry.AuditItemOperationDeleted, records[1])
		})
	}
}

func TestCreateShareLinkStoresTheLinkItReturns(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	postID := f.target(sharelink.EntityPost, sharelink.Target{})
	f.checker.Allow(policyv1.Post.ManageShareLinks(postID))
	label, password := "press preview", "share-password"

	before := time.Now()
	created, err := f.links.CreateShareLink(callerContext(t), sharelink.CreateShareLinkCommand{
		EntityType: sharelink.EntityPost, EntityID: postID, Label: &label, Password: &password,
	})
	require.NoError(t, err)
	require.Equal(t, []sharelink.Link{created}, f.store.Links())
	require.NotEmpty(t, created.ID)
	require.Len(t, created.Token, 43)
	require.Equal(t, &label, created.Label)
	require.False(t, created.CreatedAt.Before(before.Truncate(time.Microsecond)))
	require.Equal(t, created.CreatedAt.Add(7*24*time.Hour), created.ExpiresAt, "a link expires in seven days by default")
	require.NotNil(t, created.PasswordHash)
	matched, err := crypto.NewPasswordHasher(nil).Verify(password, *created.PasswordHash)
	require.NoError(t, err)
	require.True(t, matched)

	expiresAt := time.Now().Add(48 * time.Hour)
	bounded, err := f.links.CreateShareLink(callerContext(t), sharelink.CreateShareLinkCommand{EntityType: sharelink.EntityPost, EntityID: postID, ExpiresAt: &expiresAt})
	require.NoError(t, err)
	require.Equal(t, expiresAt, bounded.ExpiresAt)
	require.Nil(t, bounded.PasswordHash)
}

func TestCreateShareLinkValidatesItsInputBeforeLockingTheTarget(t *testing.T) {
	t.Parallel()
	past, tooFar := time.Now().Add(-time.Minute), time.Now().Add(366*24*time.Hour)
	empty, tooLong := "", string(make([]byte, 1025))
	for name, tc := range map[string]struct {
		command sharelink.CreateShareLinkCommand
		message string
	}{
		"an unknown target kind":     {sharelink.CreateShareLinkCommand{EntityType: "SHARE_LINK_ENTITY_TYPE_UNSPECIFIED", EntityID: uuid.NewString()}, "unsupported entity type: SHARE_LINK_ENTITY_TYPE_UNSPECIFIED"},
		"a malformed target ID":      {sharelink.CreateShareLinkCommand{EntityType: sharelink.EntityPost, EntityID: "post"}, "invalid entity_id format: must be a valid UUID"},
		"an expiry in the past":      {sharelink.CreateShareLinkCommand{EntityType: sharelink.EntityPost, EntityID: uuid.NewString(), ExpiresAt: &past}, "expires_at: must be in the future"},
		"an expiry beyond one year":  {sharelink.CreateShareLinkCommand{EntityType: sharelink.EntityPost, EntityID: uuid.NewString(), ExpiresAt: &tooFar}, "expires_at: cannot exceed 1 year from now"},
		"an empty password":          {sharelink.CreateShareLinkCommand{EntityType: sharelink.EntityPost, EntityID: uuid.NewString(), Password: &empty}, "password: must not be empty when provided"},
		"a password over 1024 bytes": {sharelink.CreateShareLinkCommand{EntityType: sharelink.EntityPost, EntityID: uuid.NewString(), Password: &tooLong}, "password: must not exceed 1024 bytes"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			_, err := f.links.CreateShareLink(callerContext(t), tc.command)
			requireMessage(t, connect.CodeInvalidArgument, tc.message, err)
			require.Empty(t, f.units.Steps())
		})
	}
}

func TestCreateShareLinkOfAnArchivedTargetRequiresEditArchived(t *testing.T) {
	t.Parallel()
	for entityType, action := range map[sharelink.EntityType]auth.ResourceAction{
		sharelink.EntityPost: policyv1.Post.EditArchived,
		sharelink.EntityWork: policyv1.Work.EditArchived,
	} {
		t.Run(string(entityType), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			id := f.target(entityType, sharelink.Target{Archived: true})
			f.checker.Allow(action(id))

			_, err := f.links.CreateShareLink(callerContext(t), sharelink.CreateShareLinkCommand{EntityType: entityType, EntityID: id})
			require.NoError(t, err)
			require.Equal(t, []authztest.Check{check(action, id)}, f.checker.Checks())
		})
	}
}

func TestCreateShareLinkMasksADeniedTargetAsMissing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	missing := uuid.NewString()
	_, err := f.links.CreateShareLink(callerContext(t), sharelink.CreateShareLinkCommand{EntityType: sharelink.EntityPost, EntityID: missing})
	requireMessage(t, connect.CodeNotFound, "post not found: "+missing, err)
	require.Empty(t, f.checker.Checks(), "a missing target answers before authorization")

	denied := f.target(sharelink.EntityPost, sharelink.Target{})
	_, err = f.links.CreateShareLink(callerContext(t), sharelink.CreateShareLinkCommand{EntityType: sharelink.EntityPost, EntityID: denied})
	requireMessage(t, connect.CodeNotFound, "post not found: "+denied, err)
	require.Equal(t, []authztest.Check{check(policyv1.Post.ManageShareLinks, denied)}, f.checker.Checks())
	require.Empty(t, f.store.Links())
	require.Empty(t, f.audit.Records())
	require.Equal(t, []mutation.Step{mutation.StepAccess, mutation.StepRollback, mutation.StepAccess, mutation.StepRollback}, f.units.Steps())
}

// A principal whose Identity was deactivated or banned keeps a session and
// its grant until both catch up; the fresh check inside the transaction
// masks the target as missing, like any caller without the action.
func TestShareLinkMutationsRejectAStaleOrBannedPrincipal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	labelID := f.target(sharelink.EntityLabel, sharelink.Target{})
	f.checker.Allow(policyv1.Label.ManageShareLinks(labelID))
	existing, err := f.links.CreateShareLink(callerContext(t), sharelink.CreateShareLinkCommand{EntityType: sharelink.EntityLabel, EntityID: labelID})
	require.NoError(t, err)
	audits := len(f.audit.Records())

	f.fence.Deactivate(callerIdentityID)
	_, err = f.links.CreateShareLink(callerContext(t), sharelink.CreateShareLinkCommand{EntityType: sharelink.EntityLabel, EntityID: labelID})
	requireMessage(t, connect.CodeNotFound, "label not found: "+labelID, err)
	err = f.links.DeleteShareLink(callerContext(t), sharelink.DeleteShareLinkCommand{ID: existing.ID})
	requireMessage(t, connect.CodeNotFound, "label not found: "+labelID, err)

	banned := principalContext(t, &auth.UserInfo{IdentityID: callerIdentityID, MemberID: callerMemberID, Authenticated: true, Onboarded: true, Banned: true})
	_, err = f.links.CreateShareLink(banned, sharelink.CreateShareLinkCommand{EntityType: sharelink.EntityLabel, EntityID: labelID})
	requireMessage(t, connect.CodePermissionDenied, "account is banned", err)

	require.Equal(t, []sharelink.Link{existing}, f.store.Links())
	require.Len(t, f.audit.Records(), audits)
}

func TestShareLinkMutationsOfALegalVersionRequireAFutureSchedule(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	privacyID := f.target(sharelink.EntityPrivacy, sharelink.Target{Version: 3})
	f.checker.Allow(policyv1.PrivacyHistory.ManageShareLinks(privacyID))
	existing := f.store.SeedLink(sharelink.Link{Token: "manual", EntityType: sharelink.EntityPrivacy, EntityID: privacyID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)})

	_, err := f.links.CreateShareLink(callerContext(t), sharelink.CreateShareLinkCommand{EntityType: sharelink.EntityPrivacy, EntityID: privacyID})
	requireMessage(t, connect.CodeFailedPrecondition, "privacy preview links require a future scheduled version", err)
	err = f.links.DeleteShareLink(callerContext(t), sharelink.DeleteShareLinkCommand{ID: existing.ID})
	requireMessage(t, connect.CodeFailedPrecondition, "privacy preview links require a future scheduled version", err)

	check := check(policyv1.PrivacyHistory.ManageShareLinks, privacyID)
	require.Equal(t, []authztest.Check{check, check}, f.checker.Checks(), "the lifecycle answers after authorization")
	require.Equal(t, []sharelink.Link{existing}, f.store.Links())
	require.Empty(t, f.audit.Records())
}

func TestCreateShareLinkRollsBackWhenItsAuditFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	auditDown := errors.New("audit store unavailable")
	f.audit.Err = auditDown
	pageID := f.target(sharelink.EntityPage, sharelink.Target{})
	f.checker.Allow(policyv1.Page.ManageShareLinks(pageID))

	_, err := f.links.CreateShareLink(callerContext(t), sharelink.CreateShareLinkCommand{EntityType: sharelink.EntityPage, EntityID: pageID})
	require.ErrorIs(t, err, auditDown)
	requireCode(t, connect.CodeInternal, err)
	require.Equal(t, []mutation.Step{mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepRollback}, f.units.Steps())
	require.Empty(t, f.store.Links())
}
