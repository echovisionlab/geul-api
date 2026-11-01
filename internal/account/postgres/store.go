// Package accountpostgres is the PostgreSQL Store of the Account domain:
// the SQL, row locks and constraint mapping of the user_deletion_request,
// account_email_change_request and account_identity rows, the Kratos reads
// through identityread fragments, and the Member steps Account runs in its
// transactions.
package accountpostgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identityread"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const userDeletionAdminFenceKey = int64(0x4745554c41444d49) // GEULADMI

var deletionPendingStates = []string{"scheduled", "recovery_confirmation_pending"}

// Bind binds the Account store to a transaction or connection another
// package opened.
func Bind(tx *gorm.DB, ports Ports) account.Store {
	return store{tx: tx, ports: ports}
}

type store struct {
	tx    *gorm.DB
	ports Ports
}

var _ account.Store = store{}

func (s store) db(ctx context.Context) *gorm.DB { return s.tx.WithContext(ctx) }

// notFoundError keeps the gorm text of a missing row while answering the
// domain sentinel.
type notFoundError struct {
	error
	sentinel error
}

func (err notFoundError) Is(target error) bool { return target == err.sentinel }
func (err notFoundError) Unwrap() error        { return err.error }

func notFound(err, sentinel error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFoundError{error: err, sentinel: sentinel}
	}
	return err
}

func linkNotFound(err error) error { return notFound(err, account.ErrMemberLinkNotFound) }

type uniqueError struct{ error }

func (err uniqueError) Is(target error) bool { return target == account.ErrEmailChangeRequestExists }
func (err uniqueError) Unwrap() error        { return err.error }

func targetOf(target authorizationtarget.Target) account.Target {
	return account.Target{MemberID: target.MemberID, IdentityID: target.IdentityID, Onboarded: target.Onboarded}
}

func (s store) RequireLinkedMember(ctx context.Context, memberID string, requireOnboarded bool) (account.Target, error) {
	target, err := authorizationtarget.RequireLinkedMember(ctx, s.tx, memberID, requireOnboarded)
	if err != nil {
		return account.Target{}, err
	}
	return targetOf(target), nil
}

func (s store) RequireLive(ctx context.Context, memberID string) (account.Target, error) {
	target, err := authorizationtarget.RequireLive(ctx, s.tx, memberID)
	if err != nil {
		return account.Target{}, err
	}
	return targetOf(target), nil
}

func (s store) LinkedMemberForMember(ctx context.Context, memberID string, requireOnboarded bool) (account.Target, error) {
	target, err := authorizationtarget.LinkedMemberForMember(s.db(ctx), memberID, requireOnboarded)
	if errors.Is(err, authorizationtarget.ErrIneligible) {
		return account.Target{}, account.ErrIneligible
	}
	if err != nil {
		return account.Target{}, err
	}
	return targetOf(target), nil
}

func (s store) ValidateActivePair(ctx context.Context, memberID, identityID string) error {
	return linkNotFound(authorizationtarget.ValidateActivePair(ctx, s.tx, memberID, identityID))
}

func (s store) ActiveMemberIDForIdentity(ctx context.Context, identityID string) (string, error) {
	memberID, err := authorizationtarget.ActiveMemberIDForIdentity(ctx, s.tx, identityID)
	return memberID, linkNotFound(err)
}

func (s store) ActiveOnboardedMemberForIdentity(ctx context.Context, identityID string) (account.Target, error) {
	target, err := authorizationtarget.ActiveOnboardedMemberForIdentity(ctx, s.tx, identityID)
	if err != nil {
		return account.Target{}, linkNotFound(err)
	}
	return targetOf(target), nil
}

func (s store) LockActivePairForCredentialMutation(ctx context.Context, memberID, identityID string) error {
	return linkNotFound(authorizationtarget.LockActivePairForCredentialMutation(ctx, s.tx, memberID, identityID))
}

func (s store) LockIdentity(ctx context.Context, identityID string) error {
	return identitystate.Lock(s.db(ctx), identityID)
}

func (s store) LockedIdentityState(ctx context.Context, identityID string) (string, error) {
	return identityread.LockedState(ctx, s.tx, identityID)
}

func (s store) IdentityExists(ctx context.Context, identityID string) (bool, error) {
	return identityread.Exists(ctx, s.tx, identityID)
}

type summaryRow struct {
	MemberID       string     `gorm:"column:member_id"`
	IdentityID     *string    `gorm:"column:identity_id"`
	Email          *string    `gorm:"column:email"`
	EmailVerified  bool       `gorm:"column:email_verified"`
	IdentityState  *string    `gorm:"column:identity_state"`
	MetadataBanned bool       `gorm:"column:metadata_banned"`
	BanReason      *string    `gorm:"column:ban_reason"`
	BanExpires     *time.Time `gorm:"column:ban_expires"`
	DeletedAt      *time.Time `gorm:"column:deleted_at"`
	PendingDelete  bool       `gorm:"column:pending_delete"`
}

func (row summaryRow) domain() account.SummaryRow {
	return account.SummaryRow{
		MemberID: row.MemberID, IdentityID: row.IdentityID, Email: row.Email, EmailVerified: row.EmailVerified,
		IdentityState: row.IdentityState, MetadataBanned: row.MetadataBanned, BanReason: row.BanReason,
		BanExpires: row.BanExpires, DeletedAt: row.DeletedAt, PendingDelete: row.PendingDelete,
	}
}

func (s store) AccountSummaries(ctx context.Context, memberIDs []string) ([]account.SummaryRow, error) {
	var rows []summaryRow
	if err := s.db(ctx).Raw(`
		SELECT
			m.id::text AS member_id,
			i.id::text AS identity_id,
			NULLIF(btrim(i.traits ->> 'email'), '') AS email,
			`+identityread.EmailVerifiedSQL("i")+` AS email_verified,
			i.state::text AS identity_state,
			COALESCE((i.metadata_admin ->> 'banned')::boolean, false) AS metadata_banned,
			NULLIF(i.metadata_admin ->> 'ban_reason', '') AS ban_reason,
			CASE WHEN NULLIF(i.metadata_admin ->> 'ban_expires', '') IS NULL THEN NULL ELSE (i.metadata_admin ->> 'ban_expires')::timestamptz END AS ban_expires,
			m.deleted_at,
			EXISTS (
				SELECT 1 FROM user_deletion_request d
				WHERE d.member_id=m.id
				  AND d.lifecycle_state IN ('scheduled', 'recovery_confirmation_pending')
				  AND d.scheduled_at IS NOT NULL
			) AS pending_delete
		FROM member AS m
		`+identityread.LeftJoinLinked("m", "i")+`
		WHERE m.id IN ?
	`, memberIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]account.SummaryRow, len(rows))
	for i, row := range rows {
		result[i] = row.domain()
	}
	return result, nil
}

func (s store) SessionSummary(ctx context.Context, memberID string) (account.SummaryRow, bool, error) {
	var row summaryRow
	result := s.db(ctx).Raw(`
		SELECT
			m.id::text AS member_id,
			i.id::text AS identity_id,
			NULLIF(btrim(i.traits ->> 'email'), '') AS email,
			i.state::text AS identity_state,
			COALESCE((i.metadata_admin ->> 'banned')::boolean, false) AS metadata_banned,
			m.deleted_at,
			EXISTS (
				SELECT 1 FROM user_deletion_request d
				WHERE d.member_id=m.id
				  AND d.lifecycle_state IN ('scheduled', 'recovery_confirmation_pending')
				  AND d.scheduled_at IS NOT NULL
			) AS pending_delete
		FROM member AS m
		`+identityread.LeftJoinLinked("m", "i")+`
		WHERE m.id = ?::uuid
	`, memberID).Scan(&row)
	if result.Error != nil {
		return account.SummaryRow{}, false, result.Error
	}
	return row.domain(), result.RowsAffected == 1, nil
}

func (s store) LiveSessions(ctx context.Context, identityID string) ([]account.Session, error) {
	type sessionRow struct {
		ID              string
		Active          bool
		AuthenticatedAt time.Time
		ExpiresAt       time.Time
	}
	var rows []sessionRow
	if err := s.db(ctx).Raw(`
		SELECT CAST(id AS text) AS id, active, authenticated_at, expires_at
		FROM `+identityread.LiveSessionsSQL+`
		ORDER BY authenticated_at DESC, id DESC
	`, identityID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	sessions := make([]account.Session, len(rows))
	for i, row := range rows {
		sessions[i] = account.Session{ID: row.ID, Active: row.Active, AuthenticatedAt: row.AuthenticatedAt, ExpiresAt: row.ExpiresAt}
	}
	return sessions, nil
}

func (s store) OwnsLiveSession(ctx context.Context, identityID, sessionID string) (bool, error) {
	var owned bool
	err := s.db(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM `+identityread.LiveSessionsSQL+` AND id = ?::uuid)`, identityID, sessionID).Scan(&owned).Error
	return owned, err
}

func (s store) OtherLiveSessionIDs(ctx context.Context, identityID, currentSessionID string) ([]string, error) {
	var sessionIDs []string
	err := s.db(ctx).Raw(`
		SELECT id::text
		FROM `+identityread.LiveSessionsSQL+`
		  AND id <> ?::uuid
		ORDER BY id
	`, identityID, currentSessionID).Scan(&sessionIDs).Error
	return sessionIDs, err
}

func deletionRequestOf(row model.UserDeletionRequest) account.DeletionRequest {
	return account.DeletionRequest{
		ID: row.ID, MemberID: row.MemberID, IdentityID: row.IdentityID, Token: row.Token, TokenExpiresAt: row.TokenExpiresAt,
		ConfirmedAt: row.ConfirmedAt, ScheduledAt: row.ScheduledAt, LifecycleState: row.LifecycleState,
		NotificationEmail: row.NotificationEmail, NotificationEmailVerifiedAt: row.NotificationEmailVerifiedAt,
		NotificationName: row.NotificationName, NotificationLocale: row.NotificationLocale,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func deletionRequestRow(request account.DeletionRequest) model.UserDeletionRequest {
	return model.UserDeletionRequest{
		ID: request.ID, MemberID: request.MemberID, IdentityID: request.IdentityID, Token: request.Token,
		TokenExpiresAt: request.TokenExpiresAt, ConfirmedAt: request.ConfirmedAt, ScheduledAt: request.ScheduledAt,
		LifecycleState: request.LifecycleState, NotificationEmail: request.NotificationEmail,
		NotificationEmailVerifiedAt: request.NotificationEmailVerifiedAt, NotificationName: request.NotificationName,
		NotificationLocale: request.NotificationLocale, CreatedAt: request.CreatedAt, UpdatedAt: request.UpdatedAt,
	}
}

func deletionRequestsOf(rows []model.UserDeletionRequest) []account.DeletionRequest {
	requests := make([]account.DeletionRequest, len(rows))
	for i, row := range rows {
		requests[i] = deletionRequestOf(row)
	}
	return requests
}

// optionalRequest answers a missing row as not found rather than an error.
func optionalRequest(row model.UserDeletionRequest, err error) (account.DeletionRequest, bool, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return account.DeletionRequest{}, false, nil
	}
	if err != nil {
		return account.DeletionRequest{}, false, err
	}
	return deletionRequestOf(row), true, nil
}

func (s store) DeletionRequestIdentityByToken(ctx context.Context, tokenHash string) (string, error) {
	var request model.UserDeletionRequest
	if err := s.db(ctx).
		Select("identity_id").
		Where("token = ?", tokenHash).
		Take(&request).Error; err != nil {
		return "", notFound(err, account.ErrDeletionRequestNotFound)
	}
	return request.IdentityID, nil
}

func (s store) LockDeletionRequestByToken(ctx context.Context, tokenHash string, now time.Time, states []string) (account.DeletionRequest, error) {
	var request model.UserDeletionRequest
	if err := s.db(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("token = ? AND token_expires_at > ? AND lifecycle_state IN ?", tokenHash, now, states).
		First(&request).Error; err != nil {
		return account.DeletionRequest{}, notFound(err, account.ErrDeletionRequestNotFound)
	}
	return deletionRequestOf(request), nil
}

func (s store) LockPairDeletionRequest(ctx context.Context, memberID, identityID string) (account.DeletionRequest, bool, error) {
	var current model.UserDeletionRequest
	err := s.db(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("member_id = ?::uuid AND identity_id = ?::uuid", memberID, identityID).
		First(&current).Error
	return optionalRequest(current, err)
}

func (s store) DeleteDeletionRequest(ctx context.Context, requestID string) error {
	return s.db(ctx).Delete(&model.UserDeletionRequest{ID: requestID}).Error
}

func (s store) DeletePairDeletionRequests(ctx context.Context, memberID, identityID string) error {
	return s.db(ctx).Where("member_id = ?::uuid AND identity_id = ?::uuid", memberID, identityID).Delete(&model.UserDeletionRequest{}).Error
}

func (s store) InsertDeletionRequest(ctx context.Context, request account.DeletionRequest) error {
	row := deletionRequestRow(request)
	return s.db(ctx).Create(&row).Error
}

func (s store) ScheduleDeletionRequest(ctx context.Context, requestID string, now, scheduledAt time.Time) error {
	return s.db(ctx).Model(&model.UserDeletionRequest{ID: requestID}).Updates(structured.Fields{
		"confirmed_at":     now,
		"scheduled_at":     scheduledAt,
		"token_expires_at": scheduledAt,
		"lifecycle_state":  "scheduled",
		"updated_at":       now,
	}).Error
}

func (s store) EndDeletionRequest(ctx context.Context, requestID, state string) error {
	return s.db(ctx).Model(&model.UserDeletionRequest{ID: requestID}).Updates(structured.Fields{
		"scheduled_at":    nil,
		"lifecycle_state": state,
		"updated_at":      gorm.Expr("GREATEST(created_at, clock_timestamp())"),
	}).Error
}

func (s store) StartRecovery(ctx context.Context, requestID, tokenHash string, tokenExpiresAt, now time.Time) error {
	return s.db(ctx).Model(&model.UserDeletionRequest{ID: requestID}).Updates(structured.Fields{
		"token":            tokenHash,
		"token_expires_at": tokenExpiresAt,
		"lifecycle_state":  "recovery_confirmation_pending",
		"updated_at":       now,
	}).Error
}

func pendingRecovery(db *gorm.DB, email string, now time.Time) *gorm.DB {
	return db.Where("lifecycle_state IN ? AND scheduled_at > ?", deletionPendingStates, now).
		Where("notification_email IS NOT NULL AND LOWER(notification_email) = ?", email)
}

func (s store) PendingRecoveryIdentity(ctx context.Context, email string, now time.Time) (string, bool, error) {
	var candidate model.UserDeletionRequest
	err := pendingRecovery(s.db(ctx), email, now).Select("identity_id").Take(&candidate).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return candidate.IdentityID, true, nil
}

func (s store) LockPendingRecovery(ctx context.Context, email string, now time.Time) (account.DeletionRequest, bool, error) {
	var request model.UserDeletionRequest
	err := pendingRecovery(s.db(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}), email, now).First(&request).Error
	return optionalRequest(request, err)
}

func (s store) DeletionRequestPair(ctx context.Context, requestID string) (string, string, bool, error) {
	var candidate model.UserDeletionRequest
	err := s.db(ctx).
		Select("member_id", "identity_id").
		Where("id = ?", requestID).
		Take(&candidate).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return candidate.MemberID, candidate.IdentityID, true, nil
}

func (s store) LockDueDeletionRequest(ctx context.Context, requestID, memberID, identityID string, now time.Time) (account.DeletionRequest, bool, error) {
	var request model.UserDeletionRequest
	err := s.db(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND member_id = ?::uuid AND identity_id = ?::uuid AND lifecycle_state IN ? AND scheduled_at IS NOT NULL AND scheduled_at <= ?", requestID, memberID, identityID, deletionPendingStates, now).
		Take(&request).Error
	return optionalRequest(request, err)
}

func (s store) DeletionRequestInStates(ctx context.Context, requestID, identityID string, states []string) (account.DeletionRequest, bool, error) {
	var current model.UserDeletionRequest
	err := s.db(ctx).Where("id = ? AND identity_id = ?::uuid AND lifecycle_state IN ?", requestID, identityID, states).
		First(&current).Error
	return optionalRequest(current, err)
}

func (s store) HasPendingDeletion(ctx context.Context, identityID string) (bool, error) {
	var pendingDeletionCount int64
	if err := s.db(ctx).Model(&model.UserDeletionRequest{}).
		Where("identity_id = ? AND lifecycle_state IN ?", identityID, deletionPendingStates).
		Count(&pendingDeletionCount).Error; err != nil {
		return false, fmt.Errorf("check pending account deletion: %w", err)
	}
	return pendingDeletionCount > 0, nil
}

func (s store) ScheduledIdentityDrift(ctx context.Context, limit int) ([]account.DeletionRequest, error) {
	var candidates []model.UserDeletionRequest
	if err := s.db(ctx).Raw(`
		SELECT request.*
		FROM user_deletion_request AS request
		`+identityread.Join("request.identity_id", "identity")+`
		WHERE request.lifecycle_state IN ('scheduled', 'recovery_confirmation_pending')
		  AND (identity.state = 'active' OR `+identityread.ActiveSessionSQL("request.identity_id")+`)
		ORDER BY request.updated_at ASC, request.id ASC
		LIMIT ?
	`, limit).Scan(&candidates).Error; err != nil {
		return nil, err
	}
	return deletionRequestsOf(candidates), nil
}

func (s store) TerminalIdentityDrift(ctx context.Context, limit int) ([]account.DeletionRequest, error) {
	var candidates []model.UserDeletionRequest
	if err := s.db(ctx).Raw(`
		SELECT request.*
		FROM user_deletion_request AS request
		`+identityread.Join("request.identity_id", "identity")+`
		WHERE request.lifecycle_state IN ('cancelled', 'recovered')
		  AND identity.state = 'inactive'
		  AND request.updated_at > identity.updated_at
		ORDER BY request.updated_at ASC, request.id ASC
		LIMIT ?
	`, limit).Scan(&candidates).Error; err != nil {
		return nil, err
	}
	return deletionRequestsOf(candidates), nil
}

func (s store) LockAdminFence(ctx context.Context) error {
	return s.db(ctx).Exec("SELECT pg_advisory_xact_lock(?)", userDeletionAdminFenceKey).Error
}

func (s store) ActiveAdminCount(ctx context.Context, identityIDs []string) (int64, error) {
	var remaining int64
	err := s.db(ctx).Raw(`
		SELECT COUNT(*)
		FROM member
		`+identityread.Join("member.account_identity_id", "identity")+`
		WHERE member.account_identity_id IN ?
		  AND member.deleted_at IS NULL
		  AND member.onboarded = TRUE
		  AND identity.state = 'active'
		  AND NOT COALESCE((identity.metadata_admin->>'banned')::boolean, false)
		  AND NOT EXISTS (
		      SELECT 1 FROM user_deletion_request AS request
		      WHERE request.member_id = member.id
		        AND request.identity_id = identity.id
		        AND request.lifecycle_state IN ?
		  )
	`, identityIDs, deletionPendingStates).Scan(&remaining).Error
	return remaining, err
}

func emailChangeRequestOf(row model.AccountEmailChangeRequest) *account.EmailChangeRequest {
	return &account.EmailChangeRequest{
		ID: row.ID, MemberID: row.MemberID, IdentityID: row.IdentityID,
		PreviousEmailAddress: row.PreviousEmailAddress, RequestedEmailAddress: row.RequestedEmailAddress, CreatedAt: row.CreatedAt,
	}
}

// optionalEmailChangeRequest answers a missing row as nil.
func optionalEmailChangeRequest(row model.AccountEmailChangeRequest, err error) (*account.EmailChangeRequest, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return emailChangeRequestOf(row), nil
}

func (s store) LockEmailChangeRequest(ctx context.Context, identityID string) (*account.EmailChangeRequest, error) {
	var active model.AccountEmailChangeRequest
	if err := s.db(ctx).Raw(`
		SELECT *
		FROM account_email_change_request
		WHERE identity_id = ?::uuid
		FOR UPDATE
	`, identityID).Scan(&active).Error; err != nil {
		return nil, err
	}
	if active.ID == "" {
		return nil, nil
	}
	return emailChangeRequestOf(active), nil
}

func (s store) InsertEmailChangeRequest(ctx context.Context, request account.EmailChangeRequest) error {
	row := model.AccountEmailChangeRequest{
		ID: request.ID, MemberID: request.MemberID, IdentityID: request.IdentityID,
		PreviousEmailAddress: request.PreviousEmailAddress, RequestedEmailAddress: request.RequestedEmailAddress, CreatedAt: request.CreatedAt,
	}
	if err := s.db(ctx).Create(&row).Error; err != nil {
		if dberrors.IsUniqueViolation(err) {
			return uniqueError{err}
		}
		return err
	}
	return nil
}

func (s store) DeleteEmailChangeRequest(ctx context.Context, requestID string) error {
	result := s.db(ctx).Delete(&model.AccountEmailChangeRequest{}, "id = ?", requestID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return notFoundError{error: gorm.ErrRecordNotFound, sentinel: account.ErrEmailChangeRequestNotFound}
	}
	return nil
}

func (s store) EmailChangeRequest(ctx context.Context, requestID string) (*account.EmailChangeRequest, error) {
	var request model.AccountEmailChangeRequest
	return optionalEmailChangeRequest(request, s.db(ctx).First(&request, "id = ?", requestID).Error)
}

func (s store) EmailChangeRequestFor(ctx context.Context, identityID, previousEmail, requestedEmail string) (*account.EmailChangeRequest, error) {
	var request model.AccountEmailChangeRequest
	err := s.db(ctx).Where(
		"identity_id = ?::uuid AND previous_email_address = ? AND requested_email_address = ?",
		identityID, previousEmail, requestedEmail,
	).First(&request).Error
	return optionalEmailChangeRequest(request, err)
}

func (s store) EmailChangeRequestTo(ctx context.Context, identityID, recipient string) (*account.EmailChangeRequest, error) {
	var request model.AccountEmailChangeRequest
	err := s.db(ctx).
		Where("identity_id = ?::uuid AND requested_email_address = ?", identityID, recipient).
		First(&request).Error
	return optionalEmailChangeRequest(request, err)
}

func (s store) LatestEmailChangeRequest(ctx context.Context) (*account.EmailChangeRequest, error) {
	var upper model.AccountEmailChangeRequest
	err := s.db(ctx).Order("created_at DESC, id DESC").First(&upper).Error
	return optionalEmailChangeRequest(upper, err)
}

func (s store) EmailChangeRequests(ctx context.Context, upper account.EmailChangeRequest, after *account.EmailChangeRequest, limit int) ([]account.EmailChangeRequest, error) {
	query := s.db(ctx).
		Where(`
				(created_at < ? OR (created_at = ? AND id <= ?::uuid))
			`, upper.CreatedAt, upper.CreatedAt, upper.ID)
	if after != nil {
		query = query.Where(`
				(created_at > ? OR (created_at = ? AND id > ?::uuid))
			`, after.CreatedAt, after.CreatedAt, after.ID)
	}
	var rows []model.AccountEmailChangeRequest
	if err := query.Order("created_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	requests := make([]account.EmailChangeRequest, len(rows))
	for i, row := range rows {
		requests[i] = *emailChangeRequestOf(row)
	}
	return requests, nil
}

func (s store) DeleteAccountIdentityAnchor(ctx context.Context, identityID string) error {
	return s.db(ctx).Exec(`DELETE FROM public.account_identity WHERE id = ?::uuid`, identityID).Error
}

func (s store) AppendRequestAudit(ctx context.Context, appender domainaudit.Appender, action sharedtelemetry.AuditAction, build domainaudit.Builder) error {
	return domainaudit.AppendRequest(ctx, s.tx, appender, action, build)
}

func (s store) AppendSystemAudit(ctx context.Context, appender domainaudit.Appender, action sharedtelemetry.AuditAction, build domainaudit.Builder) error {
	return domainaudit.AppendSystem(ctx, s.tx, appender, sharedtelemetry.ServiceBackend, action, build)
}

func (s store) AppendAuditRecord(ctx context.Context, appender domainaudit.Appender, record sharedtelemetry.AuditRecord) error {
	return appender.AppendDomainAuditInTransaction(ctx, s.tx, record)
}
