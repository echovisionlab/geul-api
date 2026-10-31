package emaildelivery

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GetEmailSuppressionQuery reads the active suppression of an address.
type GetEmailSuppressionQuery struct {
	Email string
}

// GetEmailSuppression returns the active suppression of the address, or nil
// when deliveries to it are not suppressed.
func (s *Service) GetEmailSuppression(ctx context.Context, query GetEmailSuppressionQuery) (*managev1.EmailSuppression, error) {
	// A suppression names an address and its delivery failure; only
	// administrators may read it.
	if _, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	if query.Email == "" {
		return nil, domainerr.Required("email")
	}
	suppression, err := GetActiveEmailSuppression(ctx, s.db, query.Email)
	if err != nil || suppression == nil {
		return nil, err
	}
	return toProtoEmailSuppression(suppression), nil
}

// ReleaseEmailSuppressionCommand releases the active suppression of an
// address.
type ReleaseEmailSuppressionCommand struct {
	Email string
}

// ReleaseEmailSuppression releases the active suppression of the address,
// recording the releasing Member. Releasing an address without one changes
// nothing.
func (s *Service) ReleaseEmailSuppression(ctx context.Context, command ReleaseEmailSuppressionCommand) error {
	var releasedBy *string
	if user := auth.GetUser(ctx); user != nil && user.MemberID.String() != "" {
		memberID := user.MemberID.String()
		releasedBy = &memberID
	}
	normalized := emailutil.NormalizeAddressForDelivery(command.Email)
	var suppression model.EmailSuppression
	found := false
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[bool]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			if normalized == "" {
				return nil
			}
			err := tx.WithContext(ctx).
				Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
				Where("LOWER(email) = ? AND released_at IS NULL", normalized).
				First(&suppression).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			found = err == nil
			return err
		},
		Access: freshAdmin(policyv1.EmailSuppression.Release()),
		Change: func(_ context.Context, tx *gorm.DB) (bool, error) {
			if normalized == "" {
				return false, domainerr.Required("email")
			}
			if !found {
				return false, nil
			}
			return true, tx.Model(&suppression).Updates(map[string]any{
				"released_at": time.Now().UTC(),
				"released_by": releasedBy,
			}).Error
		},
		Audit: func(released bool) mutation.Audit {
			if !released {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditEmailSuppressionUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewEmailSuppressionReleasedAuditRecord(metadata, suppression.ID)
			})
		},
	})
	return err
}

func toProtoEmailSuppression(s *model.EmailSuppression) *managev1.EmailSuppression {
	proto := &managev1.EmailSuppression{
		Id:           s.ID,
		Email:        s.Email,
		Reason:       s.Reason,
		Source:       s.Source,
		SuppressedAt: timestamppb.New(s.SuppressedAt),
		CreatedAt:    timestamppb.New(s.CreatedAt),
		UpdatedAt:    timestamppb.New(s.UpdatedAt),
	}
	if s.ReferenceID != nil {
		proto.ReferenceId = s.ReferenceID
	}
	if s.LastError != nil {
		proto.LastError = s.LastError
	}
	if s.ReleasedAt != nil {
		proto.ReleasedAt = timestamppb.New(*s.ReleasedAt)
	}
	if s.ReleasedBy != nil {
		proto.ReleasedBy = s.ReleasedBy
	}
	return proto
}
