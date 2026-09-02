package pat

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestServiceOwnsOneTokenPerMember(t *testing.T) {
	t.Parallel()
	service, repository, _ := newTestService(t, 2)

	issued, err := service.Create(t.Context(), " member-1 ")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if issued.Secret.Reveal() == "" || issued.Metadata.ID == "" || issued.Metadata.CreatedAt.IsZero() {
		t.Fatalf("issued token = %+v", issued)
	}
	if _, err := service.Create(t.Context(), "member-1"); !errors.Is(err, ErrTokenAlreadyExists) {
		t.Fatalf("Create(duplicate) error = %v, want ErrTokenAlreadyExists", err)
	}
	listed, err := service.List(t.Context(), "member-1")
	if err != nil || len(listed) != 1 || listed[0] != issued.Metadata {
		t.Fatalf("List() = %+v, %v", listed, err)
	}
	if record := repository.records[issued.Metadata.ID]; record.MemberID != "member-1" || !record.Verifier.valid() {
		t.Fatalf("stored record = %+v", record)
	}
	if other, err := service.List(t.Context(), "member-2"); err != nil || len(other) != 0 {
		t.Fatalf("List(other) = %+v, %v", other, err)
	}
}

func TestServiceAuthenticateRegenerateAndDelete(t *testing.T) {
	t.Parallel()
	service, repository, clock := newTestService(t, 2)
	issued, err := service.Create(t.Context(), "member-1")
	if err != nil {
		t.Fatal(err)
	}
	oldSecret := issued.Secret.Reveal()
	principal, err := service.Authenticate(t.Context(), oldSecret)
	if err != nil || principal.MemberID != "member-1" || principal.TokenID != issued.Metadata.ID {
		t.Fatalf("Authenticate() = %+v, %v", principal, err)
	}
	if repository.touchCalls != 1 {
		t.Fatalf("touch calls = %d", repository.touchCalls)
	}

	clock.now = clock.now.Add(time.Hour)
	regenerated, err := service.Regenerate(t.Context(), "member-1", issued.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if regenerated.Metadata.ID != issued.Metadata.ID || regenerated.Secret.Reveal() == oldSecret ||
		!regenerated.Metadata.CreatedAt.Equal(issued.Metadata.CreatedAt) ||
		!regenerated.Metadata.UpdatedAt.Equal(clock.now.UTC()) {
		t.Fatalf("Regenerate() = %+v", regenerated)
	}
	if _, err := service.Authenticate(t.Context(), oldSecret); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("old secret error = %v", err)
	}
	if _, err := service.Authenticate(t.Context(), regenerated.Secret.Reveal()); err != nil {
		t.Fatalf("new secret error = %v", err)
	}
	if err := service.Delete(t.Context(), "member-1", issued.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(t.Context(), "member-1", issued.Metadata.ID); err != nil {
		t.Fatalf("repeated Delete() error = %v", err)
	}
	if _, err := service.Authenticate(t.Context(), regenerated.Secret.Reveal()); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("deleted token error = %v", err)
	}
}

func TestServiceAuthenticationFailsClosed(t *testing.T) {
	t.Parallel()
	service, repository, _ := newTestService(t, 2)
	issued, err := service.Create(t.Context(), "member-1")
	if err != nil {
		t.Fatal(err)
	}
	raw := issued.Secret.Reveal()
	wrong := raw[:len(raw)-1] + differentLastCharacter(raw[len(raw)-1])
	for _, candidate := range []string{"not-a-token", wrong} {
		if _, err := service.Authenticate(t.Context(), candidate); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("Authenticate(%q) error = %v", candidate, err)
		}
	}
	if repository.touchCalls != 0 {
		t.Fatalf("invalid credentials touched token %d times", repository.touchCalls)
	}
	repository.touchErr = ErrTokenNotFound
	if _, err := service.Authenticate(t.Context(), raw); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("concurrent revoke error = %v", err)
	}
	repository.touchErr = errors.New("touch unavailable")
	if _, err := service.Authenticate(t.Context(), raw); err == nil || !strings.Contains(err.Error(), "touch unavailable") {
		t.Fatalf("touch dependency error = %v", err)
	}
}

func TestServiceRejectsInvalidInputsAndStoredMultiplicity(t *testing.T) {
	t.Parallel()
	service, repository, _ := newTestService(t, 2)
	if issued, err := service.Create(t.Context(), " "); !errors.Is(err, ErrInvalidInput) || issued.Secret.Reveal() != "" {
		t.Fatalf("Create(blank) = %+v, %v", issued, err)
	}
	issued, err := service.Create(t.Context(), "member-1")
	if err != nil {
		t.Fatal(err)
	}
	repository.records[TokenID("second-invalid-record")] = StoredToken{MemberID: "member-1"}
	if _, err := service.List(t.Context(), "member-1"); !errors.Is(err, ErrInvalidStoredToken) {
		t.Fatalf("List(multiple) error = %v", err)
	}
	for _, operation := range []func() error{
		func() error { _, err := service.List(t.Context(), " "); return err },
		func() error { _, err := service.Regenerate(t.Context(), "member-1", " "); return err },
		func() error { return service.Delete(t.Context(), "", issued.Metadata.ID) },
	} {
		if err := operation(); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid operation error = %v", err)
		}
	}
}

func TestServiceDoesNotReturnCredentialWhenPersistenceFails(t *testing.T) {
	t.Parallel()
	service, repository, _ := newTestService(t, 1)
	repository.createErr = errors.New("create failed")
	issued, err := service.Create(t.Context(), "member-1")
	if err == nil || !strings.Contains(err.Error(), "create failed") || issued.Secret.Reveal() != "" || issued.Metadata.ID != "" {
		t.Fatalf("Create() = %+v, %v", issued, err)
	}
}

func TestNewServiceRequiresDependenciesAndUsableClock(t *testing.T) {
	t.Parallel()
	repository := &memoryRepository{records: make(map[TokenID]StoredToken)}
	clock := &fakeClock{now: time.Now()}
	random := bytes.NewReader(make([]byte, tokenSelectorBytes+tokenSecretBytes))
	for _, test := range []struct {
		repository Repository
		clock      Clock
		random     io.Reader
	}{
		{clock: clock, random: random},
		{repository: repository, random: random},
		{repository: repository, clock: clock},
	} {
		if _, err := NewService(test.repository, test.clock, test.random); !errors.Is(err, ErrInvalidDependencies) {
			t.Fatalf("NewService() error = %v", err)
		}
	}
	broken, err := NewService(repository, &fakeClock{}, bytes.NewReader(sequentialBytes(tokenSelectorBytes+tokenSecretBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if issued, err := broken.Create(t.Context(), "member-1"); !errors.Is(err, ErrInvalidDependencies) || issued.Secret.Reveal() != "" {
		t.Fatalf("Create(zero clock) = %+v, %v", issued, err)
	}
}

type fakeClock struct{ now time.Time }

func (clock *fakeClock) Now() time.Time { return clock.now }

type memoryRepository struct {
	records      map[TokenID]StoredToken
	createErr    error
	listErr      error
	findErr      error
	replaceErr   error
	touchErr     error
	deleteErr    error
	createCalls  int
	replaceCalls int
	touchCalls   int
}

func (repository *memoryRepository) Create(_ context.Context, record StoredToken) error {
	repository.createCalls++
	if repository.createErr != nil {
		return repository.createErr
	}
	for _, existing := range repository.records {
		if existing.MemberID == record.MemberID {
			return ErrTokenAlreadyExists
		}
	}
	repository.records[record.ID] = record
	return nil
}

func (repository *memoryRepository) ListByMember(_ context.Context, memberID MemberID) ([]StoredToken, error) {
	if repository.listErr != nil {
		return nil, repository.listErr
	}
	records := make([]StoredToken, 0, 1)
	for _, record := range repository.records {
		if record.MemberID == memberID {
			records = append(records, record)
		}
	}
	slices.SortFunc(records, func(left, right StoredToken) int {
		return cmp.Compare(string(left.ID), string(right.ID))
	})
	return records, nil
}

func (repository *memoryRepository) FindByID(_ context.Context, tokenID TokenID) (StoredToken, error) {
	if repository.findErr != nil {
		return StoredToken{}, repository.findErr
	}
	record, exists := repository.records[tokenID]
	if !exists {
		return StoredToken{}, ErrTokenNotFound
	}
	return record, nil
}

func (repository *memoryRepository) ReplaceVerifier(_ context.Context, memberID MemberID, tokenID TokenID, verifier Verifier, updatedAt time.Time) (StoredToken, error) {
	repository.replaceCalls++
	if repository.replaceErr != nil {
		return StoredToken{}, repository.replaceErr
	}
	record, exists := repository.records[tokenID]
	if !exists || record.MemberID != memberID {
		return StoredToken{}, ErrTokenNotFound
	}
	record.Verifier = verifier
	record.UpdatedAt = updatedAt
	repository.records[tokenID] = record
	return record, nil
}

func (repository *memoryRepository) TouchLastUsedAt(_ context.Context, memberID MemberID, tokenID TokenID, verifier Verifier, _ time.Time) error {
	repository.touchCalls++
	if repository.touchErr != nil {
		return repository.touchErr
	}
	record, exists := repository.records[tokenID]
	if !exists || record.MemberID != memberID || !record.Verifier.matches(verifier) {
		return ErrTokenNotFound
	}
	return nil
}

func (repository *memoryRepository) Delete(_ context.Context, memberID MemberID, tokenID TokenID) error {
	if repository.deleteErr != nil {
		return repository.deleteErr
	}
	record, exists := repository.records[tokenID]
	if !exists || record.MemberID != memberID {
		return ErrTokenNotFound
	}
	delete(repository.records, tokenID)
	return nil
}

func newTestService(t *testing.T, credentialCount int) (*Service, *memoryRepository, *fakeClock) {
	t.Helper()
	repository := &memoryRepository{records: make(map[TokenID]StoredToken)}
	clock := &fakeClock{now: time.Date(2026, time.August, 23, 12, 0, 0, 0, time.FixedZone("KST", 9*60*60))}
	service, err := NewService(repository, clock, bytes.NewReader(sequentialBytes(credentialCount*(tokenSelectorBytes+tokenSecretBytes))))
	if err != nil {
		t.Fatal(err)
	}
	return service, repository, clock
}

func differentLastCharacter(value byte) string {
	if value == 'A' {
		return "B"
	}
	return "A"
}
