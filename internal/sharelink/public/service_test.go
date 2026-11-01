package public_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/crypto"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	sharelinkpublic "github.com/echovisionlab/geul-api/internal/sharelink/public"
	"github.com/echovisionlab/geul-api/internal/sharelink/sharelinktest"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

type targetKey struct {
	entityType sharelink.EntityType
	entityID   string
}

// targets resolves the targets a test sets and the automatic preview tokens
// it names.
type targets struct {
	resolved  map[targetKey]sharelinkpublic.Target
	automatic map[string]bool
	err       error
}

func (r *targets) Resolve(_ context.Context, entityType sharelink.EntityType, entityID string, _ time.Time) (sharelinkpublic.Target, error) {
	return r.resolved[targetKey{entityType, entityID}], r.err
}

func (r *targets) IsAutomaticPublicHistoryToken(_ context.Context, token string, _ sharelink.EntityType, _ string) (bool, error) {
	return r.automatic[token], nil
}

type fixture struct {
	store   *sharelinktest.Store
	targets *targets
	handler *sharelinkpublic.ConnectHandler
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, units := sharelinktest.New()
	resolver := &targets{resolved: map[targetKey]sharelinkpublic.Target{}, automatic: map[string]bool{}}
	service, err := sharelinkpublic.NewService(sharelinkpublic.Deps{Units: units, Targets: resolver})
	require.NoError(t, err)
	return &fixture{store: store, targets: resolver, handler: sharelinkpublic.NewConnectHandler(service)}
}

// link stores a link to a target the resolver finds as target.
func (f *fixture) link(entityType sharelink.EntityType, target sharelinkpublic.Target, expiresAt time.Time, passwordHash *string) sharelink.Link {
	entityID := uuid.NewString()
	f.targets.resolved[targetKey{entityType, entityID}] = target
	return f.store.SeedLink(sharelink.Link{
		Token: uuid.NewString(), EntityType: entityType, EntityID: entityID, PasswordHash: passwordHash,
		CreatedAt: expiresAt.Add(-time.Hour), ExpiresAt: expiresAt,
	})
}

func (f *fixture) validate(t *testing.T, token string, password *string) *openv1.ValidateShareLinkResponse {
	t.Helper()
	response, err := f.handler.Validate(t.Context(), connect.NewRequest(&openv1.ValidateShareLinkRequest{Token: token, Password: password}))
	require.NoError(t, err)
	return response.Msg
}

func requireResponse(t *testing.T, want, got *openv1.ValidateShareLinkResponse) {
	t.Helper()
	require.True(t, proto.Equal(want, got), "got %v, want %v", got, want)
}

func opened(link sharelink.Link, slug *string, passwordRequired bool) *openv1.ValidateShareLinkResponse {
	entityType := sharelink.EntityTypeToProto(link.EntityType)
	entityID := link.EntityID
	return &openv1.ValidateShareLinkResponse{Valid: true, EntityType: &entityType, EntityId: &entityID, Slug: slug, PasswordRequired: passwordRequired}
}

func pointer(value string) *string { return &value }

func TestValidateShareLinkOpensTheBoundedTargetOfEveryKind(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, entityType := range sharelink.EntityTypes() {
		slug := pointer(string(entityType) + "-slug")
		link := f.link(entityType, sharelinkpublic.Target{Exists: true, Slug: slug}, time.Now().Add(time.Hour), nil)
		requireResponse(t, opened(link, slug, false), f.validate(t, link.Token, nil))
	}
}

func TestValidateShareLinkOpensNothingForADeadToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	expired := f.link(sharelink.EntityPost, sharelinkpublic.Target{Exists: true}, time.Now().Add(-time.Minute), nil)
	dangling := f.link(sharelink.EntityPage, sharelinkpublic.Target{}, time.Now().Add(time.Hour), nil)
	unknownKind := f.link("not-an-enum", sharelinkpublic.Target{Exists: true}, time.Now().Add(time.Hour), nil)
	unspecified := f.link("SHARE_LINK_ENTITY_TYPE_UNSPECIFIED", sharelinkpublic.Target{Exists: true}, time.Now().Add(time.Hour), nil)

	for name, token := range map[string]string{
		"an empty token": "", "an unknown token": "missing-" + uuid.NewString(), "an expired link": expired.Token,
		"a missing target": dangling.Token, "an unknown target kind": unknownKind.Token, "an unspecified target kind": unspecified.Token,
	} {
		t.Run(name, func(t *testing.T) {
			requireResponse(t, &openv1.ValidateShareLinkResponse{}, f.validate(t, token, nil))
		})
	}
}

func TestValidateShareLinkRequiresAndVerifiesItsPassword(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	hash, err := crypto.NewPasswordHasher(nil).Hash("page-share-password")
	require.NoError(t, err)
	slug := pointer("protected-page")
	link := f.link(sharelink.EntityPage, sharelinkpublic.Target{Exists: true, Slug: slug}, time.Now().Add(time.Hour), &hash)
	locked := opened(link, slug, true)
	locked.Valid = false

	for _, supplied := range []*string{nil, pointer(""), pointer("wrong-password")} {
		requireResponse(t, locked, f.validate(t, link.Token, supplied))
	}
	requireResponse(t, opened(link, slug, true), f.validate(t, link.Token, pointer("page-share-password")))
}

// An expired legal preview link opens the now-public version only when it is
// the password-free token an update notice mailed out.
func TestValidateShareLinkOpensAnExpiredLegalPreviewOnlyFromItsNotice(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	history := sharelinkpublic.Target{Exists: true, PublicHistory: true}
	past := time.Now().Add(-time.Minute)
	automatic := f.link(sharelink.EntityPrivacy, history, past, nil)
	f.targets.automatic[automatic.Token] = true
	manual := f.link(sharelink.EntityPrivacy, history, past, nil)
	hash := sharelinktest.ArgonHash
	protected := f.link(sharelink.EntityTerms, history, past, &hash)
	f.targets.automatic[protected.Token] = true
	notPublic := f.link(sharelink.EntityTerms, sharelinkpublic.Target{Exists: true}, past, nil)
	f.targets.automatic[notPublic.Token] = true

	requireResponse(t, opened(automatic, nil, false), f.validate(t, automatic.Token, nil))
	for _, link := range []sharelink.Link{manual, protected, notPublic} {
		requireResponse(t, &openv1.ValidateShareLinkResponse{}, f.validate(t, link.Token, nil))
	}
}

func TestValidateShareLinkFailsWhenTheTargetCannotBeRead(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	link := f.link(sharelink.EntityRelease, sharelinkpublic.Target{Exists: true}, time.Now().Add(time.Hour), nil)
	unavailable := errors.New("release read failed")
	f.targets.err = unavailable

	_, err := f.handler.Validate(t.Context(), connect.NewRequest(&openv1.ValidateShareLinkRequest{Token: link.Token}))
	require.ErrorIs(t, err, unavailable)
	require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
}
