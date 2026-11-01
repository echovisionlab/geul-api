package filemedia

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func AuthorizeContentBlockFileReuse(ctx context.Context, joined Joined, checker authz.AuthorizationDecisionChecker, documentID, fileID string) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.MemberID == "" {
		return domainerr.AuthenticationRequired()
	}
	if principal.Banned {
		return domainerr.AccountBanned()
	}

	alreadyInDocument, err := joined.Store.FileAlreadyAttached(ctx, documentID, fileID)
	if err != nil {
		return fmt.Errorf("check document File reuse: %w", err)
	}
	if alreadyInDocument {
		return nil
	}

	uploader, err := joined.Store.FileUploader(ctx, fileID)
	if err != nil {
		return fmt.Errorf("read File uploader: %w", err)
	}
	if uploader != nil && *uploader == principal.MemberID.String() {
		return nil
	}
	if checker == nil {
		return fmt.Errorf("SpiceDB File reuse authorization is not configured")
	}
	can, err := policyv1.File.List()
	if err != nil {
		return err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return domainerr.NoPermission("access", "platform")
	}
	return nil
}
