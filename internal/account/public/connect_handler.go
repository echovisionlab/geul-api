package public

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/account"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AccountLifecycle is the token-authenticated deletion and recovery use
// cases of account.Service.
type AccountLifecycle interface {
	ConfirmAccountDeletion(context.Context, account.ConfirmAccountDeletionCommand) (account.AccountDeletionScheduledResult, error)
	CancelAccountDeletion(context.Context, account.CancelAccountDeletionCommand) error
	RequestAccountRecovery(context.Context, account.RequestAccountRecoveryCommand) error
	ConfirmAccountRecovery(context.Context, account.ConfirmAccountRecoveryCommand) error
}

// ConnectHandler serves the public Account Connect RPCs: the links of the
// deletion and recovery emails.
type ConnectHandler struct {
	lifecycle AccountLifecycle
}

// NewConnectHandler returns the public Connect adapter of lifecycle.
func NewConnectHandler(lifecycle AccountLifecycle) *ConnectHandler {
	return &ConnectHandler{lifecycle: lifecycle}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) ConfirmAccountDeletion(ctx context.Context, req *connect.Request[openv1.ConfirmAccountDeletionRequest]) (*connect.Response[openv1.ConfirmAccountDeletionResponse], error) {
	result, err := h.lifecycle.ConfirmAccountDeletion(ctx, account.ConfirmAccountDeletionCommand{Token: req.Msg.GetToken()})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&openv1.ConfirmAccountDeletionResponse{
		Success:     true,
		Message:     fmt.Sprintf("Account deletion confirmed. Your account will be permanently deleted on %s. You can recover your account within 30 days.", result.ScheduledAt.Format("January 2, 2006")),
		ScheduledAt: timestamppb.New(result.ScheduledAt),
	}), nil
}

func (h *ConnectHandler) CancelAccountDeletion(ctx context.Context, req *connect.Request[openv1.CancelAccountDeletionRequest]) (*connect.Response[openv1.CancelAccountDeletionResponse], error) {
	return respond(&openv1.CancelAccountDeletionResponse{
		Success: true,
		Message: "Account deletion has been cancelled. Your account is now active.",
	}, h.lifecycle.CancelAccountDeletion(ctx, account.CancelAccountDeletionCommand{Token: req.Msg.GetToken()}))
}

func (h *ConnectHandler) RequestAccountRecovery(ctx context.Context, req *connect.Request[openv1.RequestAccountRecoveryRequest]) (*connect.Response[openv1.RequestAccountRecoveryResponse], error) {
	return respond(&openv1.RequestAccountRecoveryResponse{
		Success: true,
		Message: "If an account with this email exists and is pending deletion, a recovery email has been sent.",
	}, h.lifecycle.RequestAccountRecovery(ctx, account.RequestAccountRecoveryCommand{Email: req.Msg.GetEmail()}))
}

func (h *ConnectHandler) ConfirmAccountRecovery(ctx context.Context, req *connect.Request[openv1.ConfirmAccountRecoveryRequest]) (*connect.Response[openv1.ConfirmAccountRecoveryResponse], error) {
	return respond(&openv1.ConfirmAccountRecoveryResponse{
		Success: true,
		Message: "Your account has been recovered. You can now log in again.",
	}, h.lifecycle.ConfirmAccountRecovery(ctx, account.ConfirmAccountRecoveryCommand{Token: req.Msg.GetToken()}))
}
