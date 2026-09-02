package member

import (
	"context"
	"fmt"
	"strings"

	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PrimaryEmail returns the exact linked Member delivery-primary projection.
// The Identity reverse pointer is part of every read so a stale or malformed
// pair is indistinguishable from a missing Member projection.
func PrimaryEmail(ctx context.Context, db *gorm.DB, memberID, identityID string) (string, error) {
	if !memberEmailProjectionPair(memberID, identityID) {
		return "", gorm.ErrRecordNotFound
	}
	var row struct {
		Email string `gorm:"column:primary_email"`
	}
	result := db.WithContext(ctx).
		Table("member AS member").
		Joins("JOIN kratos.identities AS identity ON identity.id = member.account_identity_id AND identity.external_id = member.id::text").
		Select("member.primary_email").
		Where("member.id = ?::uuid AND identity.id = ?::uuid AND member.deleted_at IS NULL AND member.primary_email IS NOT NULL", memberID, identityID).
		Take(&row)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected != 1 || strings.TrimSpace(row.Email) == "" {
		return "", gorm.ErrRecordNotFound
	}
	return strings.TrimSpace(row.Email), nil
}

// SyncEmailProjection locks and replaces the exact linked Member email
// projection. Candidate proof and canonical-address selection remain Account
// responsibilities; this operation owns only Member state. Inputs must
// already be canonicalized delivery addresses, and Member still fails closed
// unless primary_email is present in available_emails.
func SyncEmailProjection(
	ctx context.Context,
	tx *gorm.DB,
	memberID, identityID, primaryEmail string,
	availableEmails []string,
) error {
	if !memberEmailProjectionPair(memberID, identityID) {
		return gorm.ErrRecordNotFound
	}
	primaryEmail = emailutil.NormalizeAddressForDelivery(primaryEmail)
	if primaryEmail == "" {
		return fmt.Errorf("member primary email is required")
	}
	primaryFound := false
	for _, candidate := range availableEmails {
		if emailutil.NormalizeAddressForDelivery(candidate) != candidate {
			return fmt.Errorf("member available email must be canonicalized")
		}
		if candidate == primaryEmail {
			primaryFound = true
		}
	}
	if !primaryFound {
		return fmt.Errorf("member primary email must be an available email")
	}
	var member struct {
		ID string `gorm:"column:id"`
	}
	result := tx.WithContext(ctx).
		Table("member AS member").
		Joins("JOIN kratos.identities AS identity ON identity.id = member.account_identity_id AND identity.external_id = member.id::text").
		Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "member"}}).
		Select("member.id").
		Where("member.id = ?::uuid AND identity.id = ?::uuid AND member.deleted_at IS NULL", memberID, identityID).
		Take(&member)
	if result.Error != nil {
		return result.Error
	}
	if member.ID != memberID {
		return gorm.ErrRecordNotFound
	}
	return tx.WithContext(ctx).
		Model(&model.Member{}).
		Where("id = ?::uuid AND account_identity_id = ?::uuid AND deleted_at IS NULL", memberID, identityID).
		Updates(structured.Fields{
			"primary_email":    primaryEmail,
			"available_emails": pq.StringArray(availableEmails),
			"updated_at":       gorm.Expr("CURRENT_TIMESTAMP"),
		}).Error
}

func memberEmailProjectionPair(memberID, identityID string) bool {
	memberID = strings.TrimSpace(memberID)
	identityID = strings.TrimSpace(identityID)
	return memberID != "" && identityID != "" && memberID != identityID
}
