// Package pat owns Member personal access token issuance and verification.
package pat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

type TokenID string
type MemberID string

var (
	ErrInvalidInput        = errors.New("personal access token input is invalid")
	ErrTokenAlreadyExists  = errors.New("personal access token already exists")
	ErrTokenNotFound       = errors.New("personal access token was not found")
	ErrInvalidStoredToken  = errors.New("stored personal access token is invalid")
	ErrInvalidDependencies = errors.New("personal access token dependencies are invalid")
)

type StoredToken struct {
	ID        TokenID
	MemberID  MemberID
	Verifier  Verifier
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Metadata struct {
	ID        TokenID
	CreatedAt time.Time
	UpdatedAt time.Time
}

type IssuedToken struct {
	Metadata Metadata
	Secret   Secret
}

type Principal struct {
	MemberID MemberID
	TokenID  TokenID
}

// Repository persists one current PAT per Member. Create maps the Member
// uniqueness constraint to ErrTokenAlreadyExists.
type Repository interface {
	Create(context.Context, StoredToken) error
	ListByMember(context.Context, MemberID) ([]StoredToken, error)
	FindByID(context.Context, TokenID) (StoredToken, error)
	ReplaceVerifier(context.Context, MemberID, TokenID, Verifier, time.Time) (StoredToken, error)
	TouchLastUsedAt(context.Context, MemberID, TokenID, Verifier, time.Time) error
	Delete(context.Context, MemberID, TokenID) error
}

type Clock interface {
	Now() time.Time
}

type Service struct {
	repository Repository
	clock      Clock
	random     io.Reader
}

func NewService(repository Repository, clock Clock, random io.Reader) (*Service, error) {
	if repository == nil || clock == nil || random == nil {
		return nil, ErrInvalidDependencies
	}
	return &Service{repository: repository, clock: clock, random: random}, nil
}

// Create issues the Member's only PAT and returns its bearer exactly once.
func (service *Service) Create(ctx context.Context, memberID MemberID) (IssuedToken, error) {
	memberID, err := validateMemberID(memberID)
	if err != nil {
		return IssuedToken{}, err
	}
	credential, err := generateCredential(service.random)
	if err != nil {
		return IssuedToken{}, err
	}
	now := service.clock.Now().UTC()
	if now.IsZero() {
		return IssuedToken{}, ErrInvalidDependencies
	}
	record := StoredToken{
		ID: credential.id, MemberID: memberID, Verifier: credential.verifier,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := service.repository.Create(ctx, record); err != nil {
		return IssuedToken{}, fmt.Errorf("create personal access token: %w", err)
	}
	return IssuedToken{Metadata: metadataFromStored(record), Secret: credential.secret}, nil
}

// List returns zero or one verifier-free metadata item for the Member.
func (service *Service) List(ctx context.Context, memberID MemberID) ([]Metadata, error) {
	memberID, err := validateMemberID(memberID)
	if err != nil {
		return nil, err
	}
	records, err := service.repository.ListByMember(ctx, memberID)
	if err != nil {
		return nil, fmt.Errorf("list personal access tokens: %w", err)
	}
	if len(records) > 1 {
		return nil, ErrInvalidStoredToken
	}
	metadata := make([]Metadata, len(records))
	for index, record := range records {
		if err := validateStoredToken(record); err != nil || record.MemberID != memberID {
			return nil, ErrInvalidStoredToken
		}
		metadata[index] = metadataFromStored(record)
	}
	return metadata, nil
}

func (service *Service) Regenerate(ctx context.Context, memberID MemberID, tokenID TokenID) (IssuedToken, error) {
	memberID, tokenID, err := validateOwnedToken(memberID, tokenID)
	if err != nil {
		return IssuedToken{}, err
	}
	credential, err := generateCredentialForID(tokenID, service.random)
	if err != nil {
		return IssuedToken{}, err
	}
	now := service.clock.Now().UTC()
	if now.IsZero() {
		return IssuedToken{}, ErrInvalidDependencies
	}
	record, err := service.repository.ReplaceVerifier(ctx, memberID, tokenID, credential.verifier, now)
	if err != nil {
		return IssuedToken{}, fmt.Errorf("regenerate personal access token: %w", err)
	}
	if err := validateStoredToken(record); err != nil || record.MemberID != memberID || record.ID != tokenID ||
		!record.Verifier.matches(credential.verifier) || !record.UpdatedAt.Equal(now) {
		return IssuedToken{}, ErrInvalidStoredToken
	}
	return IssuedToken{Metadata: metadataFromStored(record), Secret: credential.secret}, nil
}

func (service *Service) Delete(ctx context.Context, memberID MemberID, tokenID TokenID) error {
	memberID, tokenID, err := validateOwnedToken(memberID, tokenID)
	if err != nil {
		return err
	}
	if err := service.repository.Delete(ctx, memberID, tokenID); err != nil {
		if errors.Is(err, ErrTokenNotFound) {
			return nil
		}
		return fmt.Errorf("delete personal access token: %w", err)
	}
	return nil
}

// Authenticate validates a PAT for a supported non-MCP API boundary.
func (service *Service) Authenticate(ctx context.Context, rawToken string) (Principal, error) {
	tokenID, candidate, err := parseToken(rawToken)
	if err != nil {
		return Principal{}, ErrInvalidToken
	}
	record, findErr := service.repository.FindByID(ctx, tokenID)
	if findErr != nil {
		var dummy Verifier
		dummy.matches(candidate)
		if errors.Is(findErr, ErrTokenNotFound) {
			return Principal{}, ErrInvalidToken
		}
		return Principal{}, fmt.Errorf("find personal access token: %w", findErr)
	}
	if !record.Verifier.matches(candidate) {
		return Principal{}, ErrInvalidToken
	}
	if err := validateStoredToken(record); err != nil || record.ID != tokenID {
		return Principal{}, ErrInvalidStoredToken
	}
	usedAt := service.clock.Now().UTC()
	if usedAt.IsZero() {
		return Principal{}, ErrInvalidDependencies
	}
	if err := service.repository.TouchLastUsedAt(ctx, record.MemberID, record.ID, record.Verifier, usedAt); err != nil {
		if errors.Is(err, ErrTokenNotFound) {
			return Principal{}, ErrInvalidToken
		}
		return Principal{}, fmt.Errorf("record personal access token use: %w", err)
	}
	return Principal{MemberID: record.MemberID, TokenID: record.ID}, nil
}

func validateMemberID(memberID MemberID) (MemberID, error) {
	memberID = MemberID(strings.TrimSpace(string(memberID)))
	if memberID == "" {
		return "", ErrInvalidInput
	}
	return memberID, nil
}

func validateOwnedToken(memberID MemberID, tokenID TokenID) (MemberID, TokenID, error) {
	memberID, err := validateMemberID(memberID)
	if err != nil || !validTokenID(tokenID) {
		return "", "", ErrInvalidInput
	}
	return memberID, tokenID, nil
}

func validateStoredToken(record StoredToken) error {
	if _, err := validateMemberID(record.MemberID); err != nil || !validTokenID(record.ID) ||
		!record.Verifier.valid() || record.CreatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
		return ErrInvalidStoredToken
	}
	return nil
}

func metadataFromStored(record StoredToken) Metadata {
	return Metadata{ID: record.ID, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
}
