// Package authenticationpostgres is the PostgreSQL authentication Store:
// the first-admin bootstrap claim, the auth code issuance ledger, and the
// Kratos session reads of the gateway and the access recorder.
package authenticationpostgres

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/identityread"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// The issuance budgets are serialized by one transaction-scoped advisory
// lock, so PostgreSQL releases it on commit, rollback, or a lost connection
// without a lease-renewal loop.
const authCodeIssuanceAdvisoryLockID int64 = 4923378199947301957

// NewUnits returns the authentication unit of work over db.
func NewUnits(db *gorm.DB) mutation.UnitOfWork[authentication.Store] {
	return mutation.Postgres(db, func(tx *gorm.DB) authentication.Store { return store{db: tx} })
}

type store struct {
	db *gorm.DB
}

func (s store) ClaimFirstAdmin(ctx context.Context, identityID, memberID string) (authentication.FirstAdminClaim, error) {
	tx := s.db.WithContext(ctx)
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "geul:first-admin-bootstrap").Error; err != nil {
		return authentication.FirstAdminClaim{}, err
	}

	// Runtime bootstrap is only valid for an explicitly empty greenfield.
	// Imported/cutover data must carry its approved first-admin result rather
	// than electing an arbitrary earliest Kratos identity at login time.
	insert := tx.Exec(
		`INSERT INTO auth_bootstrap_state (key, identity_id, member_id)
		SELECT ?, ?::uuid, ?::uuid
		WHERE EXISTS (
			SELECT 1
			FROM member
			`+identityread.JoinLinked("member", "identity")+`
			WHERE member.id = ?::uuid
			  AND identity.id = ?::uuid
			  AND member.deleted_at IS NULL
			  AND member.onboarded = TRUE
			  AND NOT `+identityread.BannedSQL("identity")+`
		)
		AND NOT EXISTS (
			SELECT 1 FROM public.account_identity
			WHERE id <> ?::uuid
		)
		ON CONFLICT (key) DO NOTHING`,
		"first_admin", identityID, memberID, memberID, identityID, identityID,
	)
	if insert.Error != nil {
		return authentication.FirstAdminClaim{}, insert.Error
	}

	var claimOwnerID string
	if err := tx.Raw(
		`SELECT COALESCE(identity_id::text, '') FROM auth_bootstrap_state WHERE key = ?`,
		"first_admin",
	).Scan(&claimOwnerID).Error; err != nil {
		return authentication.FirstAdminClaim{}, err
	}
	return authentication.FirstAdminClaim{Owner: claimOwnerID == identityID, ClaimedNow: insert.RowsAffected > 0}, nil
}

func (s store) LockIssuances(ctx context.Context) error {
	return s.db.WithContext(ctx).Exec("SELECT pg_advisory_xact_lock(?)", authCodeIssuanceAdvisoryLockID).Error
}

func (s store) IssuanceUsage(ctx context.Context, query authentication.IssuanceUsageQuery) (authentication.IssuanceUsage, error) {
	statement := "SELECT count(*), min(issued_at) FROM public.auth_code_issuance WHERE issued_at > ?"
	args := []any{query.After}
	switch query.Scope {
	case authentication.ClientIPIssuances:
		statement += " AND client_ip_digest = ?"
		args = append(args, query.Digest)
	case authentication.SubjectIssuances:
		statement += " AND subject_digest = ?"
		args = append(args, query.Digest)
	}
	var usage authentication.IssuanceUsage
	var first sql.NullTime
	if err := s.db.WithContext(ctx).Raw(statement, args...).Row().Scan(&usage.Count, &first); err != nil {
		return authentication.IssuanceUsage{}, err
	}
	if first.Valid {
		usage.First = first.Time.UTC()
	}
	return usage, nil
}

func (s store) InsertIssuance(ctx context.Context, issuance authentication.Issuance) error {
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO public.auth_code_issuance (
			token, purpose, recipient_digest, subject_digest,
			client_ip_digest, issued_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		issuance.Token,
		issuance.Purpose.String(),
		issuance.RecipientDigest,
		issuance.SubjectDigest,
		issuance.ClientIPDigest,
		issuance.IssuedAt,
		issuance.ExpiresAt,
	).Error
}

func (s store) DeleteIssuance(ctx context.Context, token string) error {
	return s.db.WithContext(ctx).Exec("DELETE FROM public.auth_code_issuance WHERE token = ?", token).Error
}

func (s store) LatestIssuance(ctx context.Context, recipientDigest string, now time.Time) (authentication.IssuedCode, bool, error) {
	var row struct {
		Token           string
		Purpose         string
		RecipientDigest string
		IssuedAt        time.Time
	}
	if err := s.db.WithContext(ctx).Raw(`
		SELECT token, purpose, recipient_digest, issued_at
		FROM public.auth_code_issuance
		WHERE recipient_digest = ? AND expires_at > ?
		ORDER BY issued_at DESC
		LIMIT 1`, recipientDigest, now).Scan(&row).Error; err != nil {
		return authentication.IssuedCode{}, false, err
	}
	if row.Token == "" {
		return authentication.IssuedCode{}, false, nil
	}
	return authentication.IssuedCode{
		Token: row.Token, Purpose: email.EventKey(row.Purpose), RecipientDigest: row.RecipientDigest, IssuedAt: row.IssuedAt.UTC(),
	}, true, nil
}
