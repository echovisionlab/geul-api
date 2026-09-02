package accountadapter

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/member/pat"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

func TestPersonalAccessTokenHandlerLifecycle(t *testing.T) {
	t.Parallel()
	handler, repository := newTestPersonalAccessTokenHandler(t)
	ctx := freshBrowserAccountContext(t.Context(), "member-1", time.Now().UTC())

	created, err := handler.CreateMyPersonalAccessToken(ctx, connect.NewRequest(&managev1.CreateMyPersonalAccessTokenRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	createdToken := created.Msg.GetPersonalAccessToken()
	if createdToken == nil || createdToken.GetId() == "" || createdToken.GetCreatedAt() == nil || created.Msg.GetSecret() == "" {
		t.Fatalf("create response = %+v", created.Msg)
	}
	if stored := repository.record(pat.TokenID(createdToken.GetId())); stored.MemberID != "member-1" {
		t.Fatalf("stored token = %+v", stored)
	}
	if duplicate, err := handler.CreateMyPersonalAccessToken(ctx, connect.NewRequest(&managev1.CreateMyPersonalAccessTokenRequest{})); connect.CodeOf(err) != connect.CodeAlreadyExists || duplicate != nil {
		t.Fatalf("duplicate create = %+v, %v", duplicate, err)
	}

	listed, err := handler.ListMyPersonalAccessTokens(ctx, connect.NewRequest(&managev1.ListMyPersonalAccessTokensRequest{}))
	if err != nil || len(listed.Msg.GetPersonalAccessTokens()) != 1 ||
		listed.Msg.GetPersonalAccessTokens()[0].GetId() != createdToken.GetId() {
		t.Fatalf("list response = %+v, %v", listed, err)
	}
	if bytes.Contains([]byte(listed.Msg.String()), []byte(created.Msg.GetSecret())) {
		t.Fatal("list response exposed one-time secret")
	}

	regenerated, err := handler.RegenerateMyPersonalAccessToken(ctx, connect.NewRequest(
		&managev1.RegenerateMyPersonalAccessTokenRequest{PersonalAccessTokenId: createdToken.GetId()},
	))
	if err != nil || regenerated.Msg.GetSecret() == "" || regenerated.Msg.GetSecret() == created.Msg.GetSecret() ||
		regenerated.Msg.GetPersonalAccessToken().GetId() != createdToken.GetId() {
		t.Fatalf("regenerate response = %+v, %v", regenerated, err)
	}
	deleted, err := handler.DeleteMyPersonalAccessToken(ctx, connect.NewRequest(
		&managev1.DeleteMyPersonalAccessTokenRequest{PersonalAccessTokenId: createdToken.GetId()},
	))
	if err != nil || !deleted.Msg.GetDeleted() {
		t.Fatalf("delete response = %+v, %v", deleted, err)
	}
	deleted, err = handler.DeleteMyPersonalAccessToken(ctx, connect.NewRequest(
		&managev1.DeleteMyPersonalAccessTokenRequest{PersonalAccessTokenId: createdToken.GetId()},
	))
	if err != nil || !deleted.Msg.GetDeleted() {
		t.Fatalf("repeated delete response = %+v, %v", deleted, err)
	}
}

func TestPersonalAccessTokenHandlerAdminTargetLifecycle(t *testing.T) {
	t.Parallel()
	handler, repository := newTestPersonalAccessTokenHandler(t)
	ctx := freshBrowserAccountContext(t.Context(), "admin-member", time.Now().UTC())
	targetMemberID := "22222222-2222-4222-8222-222222222222"

	created, err := handler.CreateAccountPersonalAccessToken(ctx, connect.NewRequest(
		&managev1.CreateAccountPersonalAccessTokenRequest{MemberId: targetMemberID},
	))
	if err != nil {
		t.Fatal(err)
	}
	token := created.Msg.GetPersonalAccessToken()
	if token == nil || token.GetId() == "" || created.Msg.GetSecret() == "" {
		t.Fatalf("create response = %+v", created.Msg)
	}
	if stored := repository.record(pat.TokenID(token.GetId())); stored.MemberID != pat.MemberID(targetMemberID) {
		t.Fatalf("stored token = %+v", stored)
	}

	listed, err := handler.ListAccountPersonalAccessTokens(ctx, connect.NewRequest(
		&managev1.ListAccountPersonalAccessTokensRequest{MemberId: targetMemberID},
	))
	if err != nil || len(listed.Msg.GetPersonalAccessTokens()) != 1 ||
		listed.Msg.GetPersonalAccessTokens()[0].GetId() != token.GetId() {
		t.Fatalf("list response = %+v, %v", listed, err)
	}

	regenerated, err := handler.RegenerateAccountPersonalAccessToken(ctx, connect.NewRequest(
		&managev1.RegenerateAccountPersonalAccessTokenRequest{
			MemberId: targetMemberID, PersonalAccessTokenId: token.GetId(),
		},
	))
	if err != nil || regenerated.Msg.GetSecret() == "" || regenerated.Msg.GetSecret() == created.Msg.GetSecret() {
		t.Fatalf("regenerate response = %+v, %v", regenerated, err)
	}

	deleted, err := handler.DeleteAccountPersonalAccessToken(ctx, connect.NewRequest(
		&managev1.DeleteAccountPersonalAccessTokenRequest{
			MemberId: targetMemberID, PersonalAccessTokenId: token.GetId(),
		},
	))
	if err != nil || !deleted.Msg.GetDeleted() {
		t.Fatalf("delete response = %+v, %v", deleted, err)
	}
}

func TestPersonalAccessTokenHandlerAdminTargetRequiresCanonicalMemberAndFreshBrowser(t *testing.T) {
	t.Parallel()
	handler, repository := newTestPersonalAccessTokenHandler(t)
	now := time.Now().UTC()

	response, err := handler.CreateAccountPersonalAccessToken(
		freshBrowserAccountContext(t.Context(), "admin-member", now),
		connect.NewRequest(&managev1.CreateAccountPersonalAccessTokenRequest{MemberId: "not-a-member-id"}),
	)
	if connect.CodeOf(err) != connect.CodeInvalidArgument || response != nil {
		t.Fatalf("invalid target = %+v, %v", response, err)
	}

	response, err = handler.CreateAccountPersonalAccessToken(
		freshBrowserAccountContext(t.Context(), "admin-member", now.Add(-24*time.Hour)),
		connect.NewRequest(&managev1.CreateAccountPersonalAccessTokenRequest{
			MemberId: "22222222-2222-4222-8222-222222222222",
		}),
	)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || response != nil {
		t.Fatalf("stale admin = %+v, %v", response, err)
	}
	if repository.calls() != 0 {
		t.Fatalf("rejected request reached repository %d times", repository.calls())
	}
}

func TestPersonalAccessTokenHandlerRequiresBrowserSessionAndFreshness(t *testing.T) {
	t.Parallel()
	handler, repository := newTestPersonalAccessTokenHandler(t)
	now := time.Now().UTC()
	for _, test := range []struct {
		name string
		ctx  context.Context
		code connect.Code
	}{
		{name: "unauthenticated", ctx: t.Context(), code: connect.CodeUnauthenticated},
		{name: "machine principal", ctx: auth.WithUser(t.Context(), &auth.UserInfo{
			IdentityID: "11111111-1111-4111-8111-111111111111", MemberID: "member-1",
			AuthenticatedAt: now, Authenticated: true, Onboarded: true,
		}), code: connect.CodeUnauthenticated},
		{name: "stale browser", ctx: freshBrowserAccountContext(t.Context(), "member-1", now.Add(-24*time.Hour)), code: connect.CodeFailedPrecondition},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := handler.CreateMyPersonalAccessToken(test.ctx, connect.NewRequest(&managev1.CreateMyPersonalAccessTokenRequest{}))
			if connect.CodeOf(err) != test.code || response != nil {
				t.Fatalf("Create() = %+v, %v", response, err)
			}
		})
	}
	if repository.calls() != 0 {
		t.Fatalf("rejected request reached repository %d times", repository.calls())
	}
}

func TestPersonalAccessTokenHandlerMapsInvalidAndMissingTokenIDs(t *testing.T) {
	t.Parallel()
	handler, _ := newTestPersonalAccessTokenHandler(t)
	ctx := freshBrowserAccountContext(t.Context(), "member-1", time.Now().UTC())
	if response, err := handler.RegenerateMyPersonalAccessToken(ctx, connect.NewRequest(
		&managev1.RegenerateMyPersonalAccessTokenRequest{PersonalAccessTokenId: " "},
	)); connect.CodeOf(err) != connect.CodeInvalidArgument || response != nil {
		t.Fatalf("Regenerate(invalid) = %+v, %v", response, err)
	}
	missingID := "AQEBAQEBAQEBAQEBAQEBAQ"
	if response, err := handler.RegenerateMyPersonalAccessToken(ctx, connect.NewRequest(
		&managev1.RegenerateMyPersonalAccessTokenRequest{PersonalAccessTokenId: missingID},
	)); connect.CodeOf(err) != connect.CodeNotFound || response != nil {
		t.Fatalf("Regenerate(missing) = %+v, %v", response, err)
	}
}

func TestNewPersonalAccessTokenHandlerRequiresDependenciesAndDelegates(t *testing.T) {
	t.Parallel()
	delegate := managev1connect.UnimplementedAccountServiceHandler{}
	tokens := mustAccountPATService(t, newAccountPATRepository())
	if handler, err := NewPersonalAccessTokenHandler(nil, tokens); err == nil || handler != nil {
		t.Fatalf("nil delegate = %T, %v", handler, err)
	}
	if handler, err := NewPersonalAccessTokenHandler(delegate, nil); err == nil || handler != nil {
		t.Fatalf("nil service = %T, %v", handler, err)
	}
	handler, err := NewPersonalAccessTokenHandler(delegate, tokens)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.GetMySecurity(t.Context(), connect.NewRequest(&managev1.GetMySecurityRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("delegated GetMySecurity() error = %v", err)
	}
}

func freshBrowserAccountContext(ctx context.Context, memberID string, authenticatedAt time.Time) context.Context {
	return auth.WithUser(ctx, &auth.UserInfo{
		IdentityID:      "11111111-1111-4111-8111-111111111111",
		MemberID:        auth.MemberID(memberID),
		SessionID:       auth.SessionID("session-" + memberID),
		AuthenticatedAt: authenticatedAt,
		Authenticated:   true,
		Onboarded:       true,
	})
}

type accountPATClock struct{ now time.Time }

func (clock accountPATClock) Now() time.Time { return clock.now }

type accountPATRepository struct {
	mu           sync.Mutex
	records      map[pat.TokenID]pat.StoredToken
	createCalls  int
	listCalls    int
	replaceCalls int
	touchCalls   int
	deleteCalls  int
}

func newAccountPATRepository() *accountPATRepository {
	return &accountPATRepository{records: make(map[pat.TokenID]pat.StoredToken)}
}

func (repository *accountPATRepository) Create(_ context.Context, record pat.StoredToken) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.createCalls++
	for _, existing := range repository.records {
		if existing.MemberID == record.MemberID {
			return pat.ErrTokenAlreadyExists
		}
	}
	repository.records[record.ID] = record
	return nil
}

func (repository *accountPATRepository) ListByMember(_ context.Context, memberID pat.MemberID) ([]pat.StoredToken, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.listCalls++
	result := make([]pat.StoredToken, 0, 1)
	for _, record := range repository.records {
		if record.MemberID == memberID {
			result = append(result, record)
		}
	}
	return result, nil
}

func (repository *accountPATRepository) FindByID(_ context.Context, tokenID pat.TokenID) (pat.StoredToken, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	record, exists := repository.records[tokenID]
	if !exists {
		return pat.StoredToken{}, pat.ErrTokenNotFound
	}
	return record, nil
}

func (repository *accountPATRepository) ReplaceVerifier(_ context.Context, memberID pat.MemberID, tokenID pat.TokenID, verifier pat.Verifier, updatedAt time.Time) (pat.StoredToken, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.replaceCalls++
	record, exists := repository.records[tokenID]
	if !exists || record.MemberID != memberID {
		return pat.StoredToken{}, pat.ErrTokenNotFound
	}
	record.Verifier = verifier
	record.UpdatedAt = updatedAt
	repository.records[tokenID] = record
	return record, nil
}

func (repository *accountPATRepository) TouchLastUsedAt(_ context.Context, memberID pat.MemberID, tokenID pat.TokenID, verifier pat.Verifier, _ time.Time) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.touchCalls++
	record, exists := repository.records[tokenID]
	if !exists || record.MemberID != memberID || !bytes.Equal(record.Verifier.Bytes(), verifier.Bytes()) {
		return pat.ErrTokenNotFound
	}
	return nil
}

func (repository *accountPATRepository) Delete(_ context.Context, memberID pat.MemberID, tokenID pat.TokenID) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.deleteCalls++
	record, exists := repository.records[tokenID]
	if !exists || record.MemberID != memberID {
		return pat.ErrTokenNotFound
	}
	delete(repository.records, tokenID)
	return nil
}

func (repository *accountPATRepository) record(tokenID pat.TokenID) pat.StoredToken {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.records[tokenID]
}

func (repository *accountPATRepository) calls() int {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.createCalls + repository.listCalls + repository.replaceCalls + repository.touchCalls + repository.deleteCalls
}

func newTestPersonalAccessTokenHandler(t *testing.T) (*PersonalAccessTokenHandler, *accountPATRepository) {
	t.Helper()
	repository := newAccountPATRepository()
	handler, err := NewPersonalAccessTokenHandler(
		managev1connect.UnimplementedAccountServiceHandler{},
		mustAccountPATService(t, repository),
	)
	if err != nil {
		t.Fatal(err)
	}
	return handler, repository
}

func mustAccountPATService(t *testing.T, repository pat.Repository) *pat.Service {
	t.Helper()
	random := make([]byte, 0, 48*3)
	for value := byte(1); value <= 3; value++ {
		random = append(random, bytes.Repeat([]byte{value}, 48)...)
	}
	service, err := pat.NewService(
		repository,
		accountPATClock{now: time.Date(2026, time.August, 23, 1, 2, 3, 0, time.UTC)},
		bytes.NewReader(random),
	)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

var _ pat.Repository = (*accountPATRepository)(nil)
