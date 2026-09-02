package member

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (DeletionLifecycle) PrepareTombstone(ctx context.Context, db *gorm.DB, memberID, identityID, primaryEmailSnapshot string) (DeletionSnapshot, error) {
	result := DeletionSnapshot{MemberID: memberID, IdentityID: identityID, PrimaryEmailSnapshot: emailutil.NormalizeAddressForDelivery(primaryEmailSnapshot)}
	if err := validateDeletionPair(memberID, identityID); err != nil {
		return result, err
	}
	if result.PrimaryEmailSnapshot == "" {
		return result, fmt.Errorf("user deletion identity command requires Member primary email snapshot")
	}
	var member model.Member
	if err := db.WithContext(ctx).Select("id", "account_identity_id", "primary_email", "available_emails", "deleted_at").Where("id = ?::uuid", memberID).Take(&member).Error; err != nil {
		return result, fmt.Errorf("load Member email projection before identity deletion: %w", err)
	}
	result.AlreadyTombstoned = member.DeletedAt != nil
	result.IdentityLinked = member.AccountIdentityID != nil
	if result.AlreadyTombstoned && result.IdentityLinked {
		return result, fmt.Errorf("deleted Member retains an identity reverse pointer")
	}
	if !result.AlreadyTombstoned && result.IdentityLinked && strings.TrimSpace(*member.AccountIdentityID) != identityID {
		return result, fmt.Errorf("member reverse pointer does not match deletion identity")
	}
	if !result.AlreadyTombstoned && result.IdentityLinked {
		linked, identityExists, err := deletionPairWitness(ctx, db, memberID, identityID)
		if err != nil {
			return result, err
		}
		if identityExists && !linked {
			return result, fmt.Errorf("member and identity deletion link is not bilateral")
		}
	}
	if result.AlreadyTombstoned {
		return result, nil
	}
	if member.PrimaryEmail == nil || !memberEmailsEqual(*member.PrimaryEmail, result.PrimaryEmailSnapshot) || !slices.Contains(member.AvailableEmails, result.PrimaryEmailSnapshot) {
		return result, fmt.Errorf("deletion email snapshot does not match the Member email projection")
	}
	return result, nil
}

func (DeletionLifecycle) FinalizeTombstone(ctx context.Context, db *gorm.DB, request DeletionSnapshot, deletedAt time.Time, appendAudit DeletionAudit) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var identityExists bool
		if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM kratos.identities WHERE id = ?::uuid)`, request.IdentityID).Scan(&identityExists).Error; err != nil {
			return err
		}
		if identityExists {
			return fmt.Errorf("exact Kratos identity still exists after deletion")
		}
		var member model.Member
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?::uuid", request.MemberID).Take(&member).Error; err != nil {
			return err
		}
		if member.AccountIdentityID != nil {
			return fmt.Errorf("member reverse pointer was not cleared by exact identity deletion")
		}
		if err := tx.Exec(
			`DELETE FROM member_personal_access_token WHERE member_id = ?::uuid`,
			request.MemberID,
		).Error; err != nil {
			return fmt.Errorf("delete Member personal access tokens: %w", err)
		}
		if member.DeletedAt == nil {
			if err := tx.Model(&model.Member{}).Where("id = ?::uuid AND account_identity_id IS NULL AND deleted_at IS NULL", member.ID).Updates(structured.Fields{"bio": nil, "website": nil, "social_links": gorm.Expr(`'{}'::jsonb`), "preferred_locale": nil, "deleted_at": deletedAt, "updated_at": deletedAt}).Error; err != nil {
				return err
			}
			member.DeletedAt = &deletedAt
		}
		if member.DeletedAt == nil {
			return fmt.Errorf("member tombstone was not established")
		}
		if !request.AlreadyTombstoned && (member.PrimaryEmail == nil || !memberEmailsEqual(*member.PrimaryEmail, request.PrimaryEmailSnapshot) || !slices.Contains(member.AvailableEmails, request.PrimaryEmailSnapshot)) {
			return fmt.Errorf("member email projection changed during identity deletion")
		}
		if !request.AlreadyTombstoned && appendAudit != nil {
			return appendAudit(ctx, tx, request.MemberID)
		}
		return nil
	})
}

func (DeletionLifecycle) NotificationSnapshot(ctx context.Context, db *gorm.DB, memberID, identityID string) (DeletionNotification, error) {
	if err := validateDeletionPair(memberID, identityID); err != nil {
		return DeletionNotification{}, err
	}
	var member model.Member
	if err := db.WithContext(ctx).Select("nickname", "primary_email", "preferred_locale").Where("id = ?::uuid AND account_identity_id = ?::uuid AND deleted_at IS NULL", memberID, identityID).Take(&member).Error; err != nil {
		return DeletionNotification{}, err
	}
	result := DeletionNotification{Nickname: strings.TrimSpace(member.Nickname)}
	if member.PrimaryEmail != nil {
		result.PrimaryEmail = emailutil.NormalizeAddressForDelivery(*member.PrimaryEmail)
	}
	if member.PreferredLocale != nil {
		locale := strings.TrimSpace(*member.PreferredLocale)
		if locale != "" {
			result.Locale = &locale
		}
	}
	return result, nil
}

func (DeletionLifecycle) CompletionEligible(ctx context.Context, db *gorm.DB, memberID string) (bool, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return false, err
	}
	var found bool
	if err := db.WithContext(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM member WHERE id = ?::uuid AND deleted_at IS NOT NULL AND account_identity_id IS NULL AND onboarded = TRUE)`, memberID).Scan(&found).Error; err != nil {
		return false, err
	}
	return found, nil
}
