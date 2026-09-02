package emaildelivery

import (
	"context"
	"strings"
	"time"

	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	EmailSuppressionReasonInvalidRecipient = "invalid_recipient"
	EmailSuppressionReasonManual           = "manual"
	EmailSuppressionReasonSESBounce        = "bounce"
	EmailSuppressionReasonSESComplaint     = "complaint"

	EmailSuppressionSourceEmailWorker = "email_worker"
	EmailSuppressionSourceAdmin       = "admin"
	EmailSuppressionSourceSESCallback = "ses_callback"
)

func GetActiveEmailSuppression(ctx context.Context, db *gorm.DB, email string) (*model.EmailSuppression, error) {
	normalized := emailutil.NormalizeAddressForDelivery(email)
	if normalized == "" {
		return nil, nil
	}

	var suppression model.EmailSuppression
	err := db.WithContext(ctx).
		Where("LOWER(email) = ? AND released_at IS NULL", normalized).
		First(&suppression).Error
	if err == nil {
		return &suppression, nil
	}
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return nil, err
}

func LookupActiveEmailSuppressionsByEmail(ctx context.Context, db *gorm.DB, emails []string) (map[string]model.EmailSuppression, error) {
	normalizedEmails := normalizeEmailSuppressionList(emails)
	if len(normalizedEmails) == 0 {
		return map[string]model.EmailSuppression{}, nil
	}

	var rows []model.EmailSuppression
	if err := db.WithContext(ctx).
		Where("LOWER(email) IN ? AND released_at IS NULL", normalizedEmails).
		Find(&rows).Error; err != nil {
		return nil, err
	}

	result := make(map[string]model.EmailSuppression, len(rows))
	for _, row := range rows {
		result[emailutil.NormalizeAddressForDelivery(row.Email)] = row
	}
	return result, nil
}

func SuppressEmailAddress(
	ctx context.Context,
	db *gorm.DB,
	email string,
	reason string,
	source string,
	referenceID *string,
	message string,
) error {
	normalized := emailutil.NormalizeAddressForDelivery(email)
	if normalized == "" {
		return nil
	}
	if reason == "" {
		reason = EmailSuppressionReasonInvalidRecipient
	}
	if source == "" {
		source = EmailSuppressionSourceEmailWorker
	}

	now := time.Now()
	var lastError *string
	if strings.TrimSpace(message) != "" {
		value := strings.TrimSpace(message)
		lastError = &value
	}

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if source == EmailSuppressionSourceSESCallback &&
			referenceID != nil && strings.TrimSpace(*referenceID) != "" {
			var replayCount int64
			if err := tx.Model(&model.EmailSuppression{}).
				Where(
					"LOWER(email) = ? AND source = ? AND reason = ? AND reference_id = ?",
					normalized,
					source,
					reason,
					strings.TrimSpace(*referenceID),
				).
				Count(&replayCount).Error; err != nil {
				return err
			}
			if replayCount > 0 {
				return nil
			}
		}

		var existing model.EmailSuppression
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("LOWER(email) = ? AND released_at IS NULL", normalized).
			First(&existing).Error
		if err == nil {
			if source == EmailSuppressionSourceSESCallback &&
				!shouldApplySESSuppression(existing, reason) {
				return nil
			}
			if existing.Reason == reason &&
				existing.Source == source &&
				stringPointerEqual(existing.ReferenceID, referenceID) {
				return nil
			}
			return tx.Model(&existing).Updates(structured.Fields{
				"reason":        reason,
				"source":        source,
				"reference_id":  referenceID,
				"last_error":    lastError,
				"suppressed_at": now,
			}).Error
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}

		return tx.Create(&model.EmailSuppression{
			Email:        normalized,
			Reason:       reason,
			Source:       source,
			ReferenceID:  referenceID,
			LastError:    lastError,
			SuppressedAt: now,
		}).Error
	})
}

func shouldApplySESSuppression(existing model.EmailSuppression, incomingReason string) bool {
	if existing.Source == EmailSuppressionSourceAdmin ||
		existing.Reason == EmailSuppressionReasonManual {
		return false
	}

	return emailSuppressionSeverity(incomingReason) > emailSuppressionSeverity(existing.Reason)
}

func emailSuppressionSeverity(reason string) int {
	switch reason {
	case EmailSuppressionReasonManual:
		return 40
	case EmailSuppressionReasonSESComplaint:
		return 30
	case EmailSuppressionReasonSESBounce:
		return 20
	case EmailSuppressionReasonInvalidRecipient:
		return 10
	default:
		return 0
	}
}

func stringPointerEqual(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.TrimSpace(*left) == strings.TrimSpace(*right)
}

func normalizeEmailSuppressionList(emails []string) []string {
	seen := make(map[string]struct{}, len(emails))
	result := make([]string, 0, len(emails))
	for _, email := range emails {
		normalized := emailutil.NormalizeAddressForDelivery(email)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}

func normalizeSuppressionAddress(value string) string {
	return emailutil.NormalizeAddressForDelivery(value)
}
