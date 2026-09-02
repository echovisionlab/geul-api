package public

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

type accountLifecycle interface {
	ConfirmDeletion(context.Context, string) (*account.AccountDeletionScheduledResult, error)
	CancelDeletion(context.Context, string) error
	RequestRecovery(context.Context, string) error
	ConfirmRecovery(context.Context, string) error
}

// AccountService owns token-authenticated account lifecycle endpoints.
type AccountService struct {
	openv1connect.UnimplementedAccountServiceHandler
	lifecycle accountLifecycle
}

func NewAuditedAccountService(
	db *gorm.DB,
	kratosClient auth.IdentityManager,
	spiceDB *auth.SpiceDBClient,
	baseURL string,
	emailPublisher account.EmailCommandPublisher,
	memberDeletion account.MemberDeletionLifecycle,
	memberEmails account.MemberEmailProjection,
	auditWriter interface {
		AppendDomainAuditInTransaction(context.Context, *gorm.DB, sharedtelemetry.AuditRecord) error
	},
) *AccountService {
	if db == nil || kratosClient == nil || spiceDB == nil || emailPublisher == nil || memberDeletion == nil || memberEmails == nil || auditWriter == nil {
		panic("public account service dependencies are required")
	}
	return &AccountService{
		lifecycle: account.NewAuditedAccountLifecycleService(
			db,
			kratosClient,
			spiceDB,
			baseURL,
			emailPublisher,
			auditWriter,
			account.WithLifecycleMemberDeletion(memberDeletion),
			account.WithLifecycleMemberEmailProjection(memberEmails),
		),
	}
}

func (s *AccountService) ConfirmAccountDeletion(
	ctx context.Context,
	req *connect.Request[openv1.ConfirmAccountDeletionRequest],
) (*connect.Response[openv1.ConfirmAccountDeletionResponse], error) {
	result, err := s.lifecycle.ConfirmDeletion(ctx, req.Msg.GetToken())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&openv1.ConfirmAccountDeletionResponse{
		Success:     true,
		Message:     fmt.Sprintf("Account deletion confirmed. Your account will be permanently deleted on %s. You can recover your account within 30 days.", result.ScheduledAt.Format("January 2, 2006")),
		ScheduledAt: timestamppb.New(result.ScheduledAt),
	}), nil
}

func (s *AccountService) CancelAccountDeletion(
	ctx context.Context,
	req *connect.Request[openv1.CancelAccountDeletionRequest],
) (*connect.Response[openv1.CancelAccountDeletionResponse], error) {
	if err := s.lifecycle.CancelDeletion(ctx, req.Msg.GetToken()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&openv1.CancelAccountDeletionResponse{
		Success: true,
		Message: "Account deletion has been cancelled. Your account is now active.",
	}), nil
}

func (s *AccountService) RequestAccountRecovery(
	ctx context.Context,
	req *connect.Request[openv1.RequestAccountRecoveryRequest],
) (*connect.Response[openv1.RequestAccountRecoveryResponse], error) {
	if err := s.lifecycle.RequestRecovery(ctx, req.Msg.GetEmail()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&openv1.RequestAccountRecoveryResponse{
		Success: true,
		Message: "If an account with this email exists and is pending deletion, a recovery email has been sent.",
	}), nil
}

func (s *AccountService) ConfirmAccountRecovery(
	ctx context.Context,
	req *connect.Request[openv1.ConfirmAccountRecoveryRequest],
) (*connect.Response[openv1.ConfirmAccountRecoveryResponse], error) {
	if err := s.lifecycle.ConfirmRecovery(ctx, req.Msg.GetToken()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&openv1.ConfirmAccountRecoveryResponse{
		Success: true,
		Message: "Your account has been recovered. You can now log in again.",
	}), nil
}
