package emaildelivery

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type EmailSuppressionService struct {
	managev1connect.UnimplementedEmailSuppressionServiceHandler
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	auditWriter domainaudit.Appender
}

// NewAuditedEmailSuppressionService creates an EmailSuppressionService whose
// administrator release operation appends Domain Audit in the same transaction.
func NewAuditedEmailSuppressionService(db *gorm.DB, auditWriter domainaudit.Appender, spiceDB *auth.SpiceDBClient) *EmailSuppressionService {
	if auditWriter == nil {
		panic("email suppression audit writer is required")
	}
	service := NewEmailSuppressionService(db, spiceDB)
	service.auditWriter = auditWriter
	return service
}

func NewEmailSuppressionService(db *gorm.DB, spiceDB *auth.SpiceDBClient) *EmailSuppressionService {
	dependencycheck.MustNotNil(db, "db")
	dependencycheck.MustNotNil(spiceDB, "spiceDB")
	return &EmailSuppressionService{db: db, spiceDB: spiceDB}
}

func (s *EmailSuppressionService) GetEmailSuppression(
	ctx context.Context,
	req *connect.Request[managev1.GetEmailSuppressionRequest],
) (*connect.Response[managev1.GetEmailSuppressionResponse], error) {
	if req.Msg.Email == "" {
		return nil, errs.Required("email")
	}

	suppression, err := GetActiveEmailSuppression(ctx, s.db, req.Msg.Email)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if suppression == nil {
		return connect.NewResponse(&managev1.GetEmailSuppressionResponse{}), nil
	}
	return connect.NewResponse(&managev1.GetEmailSuppressionResponse{
		Suppression: toProtoEmailSuppression(suppression),
	}), nil
}

func (s *EmailSuppressionService) ReleaseEmailSuppression(
	ctx context.Context,
	req *connect.Request[managev1.ReleaseEmailSuppressionRequest],
) (*connect.Response[managev1.ReleaseEmailSuppressionResponse], error) {
	can, err := policyv1.EmailSuppression.Release()
	if err != nil {
		return nil, errs.Internal(err)
	}

	var releasedBy *string
	if user := auth.GetUser(ctx); user != nil && user.MemberID.String() != "" {
		memberID := user.MemberID.String()
		releasedBy = &memberID
	}
	if err := s.releaseEmailSuppression(ctx, req.Msg.Email, releasedBy, can); err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.ReleaseEmailSuppressionResponse{Success: true}), nil
}

func (s *EmailSuppressionService) releaseEmailSuppression(
	ctx context.Context,
	email string,
	releasedBy *string,
	can policyv1.Can,
) error {
	normalized := emailutil.NormalizeAddressForDelivery(email)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var suppression model.EmailSuppression
		findErr := gorm.ErrRecordNotFound
		if normalized != "" {
			findErr = tx.WithContext(ctx).
				Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("LOWER(email) = ? AND released_at IS NULL", normalized).
				First(&suppression).Error
		}
		if findErr != nil && findErr != gorm.ErrRecordNotFound {
			return findErr
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if normalized == "" {
			return errs.Required("email")
		}
		if findErr == gorm.ErrRecordNotFound {
			return nil
		}
		now := time.Now().UTC()
		if err := tx.Model(&suppression).Updates(map[string]any{
			"released_at": now,
			"released_by": releasedBy,
		}).Error; err != nil {
			return err
		}
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditEmailSuppressionUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewEmailSuppressionReleasedAuditRecord(metadata, suppression.ID)
		})
	})
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
