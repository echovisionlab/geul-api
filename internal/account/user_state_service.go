package account

import (
	"context"
	"maps"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
)

type UserStateService struct {
	identityManager auth.IdentityManager
}

func NewUserStateService(identityManager auth.IdentityManager) *UserStateService {
	if identityManager == nil {
		panic("identityManager is required")
	}
	return &UserStateService{identityManager: identityManager}
}

func (s *UserStateService) Ban(ctx context.Context, userID string, reason *string, until *time.Time) error {
	identity, err := s.identityManager.GetIdentity(ctx, userID)
	if err != nil {
		return errs.Internal(err)
	}
	if identity == nil {
		return errs.InternalMsg("identity is required")
	}
	metadataAdmin := cloneIdentityMetadataAdmin(identity.MetadataAdmin)
	metadataAdmin["banned"] = true
	if reason != nil {
		metadataAdmin["ban_reason"] = *reason
	} else {
		delete(metadataAdmin, "ban_reason")
	}
	if until != nil {
		metadataAdmin["ban_expires"] = until.Format(time.RFC3339)
	} else {
		delete(metadataAdmin, "ban_expires")
	}

	// Identity inactivity is the fail-closed authority. Metadata is descriptive
	// and must never become the only persisted ban fact while sessions remain
	// usable after a partial failure.
	if err := s.identityManager.SetIdentityState(ctx, userID, auth.KratosStateInactive); err != nil {
		return errs.Internal(err)
	}
	if err := s.identityManager.UpdateIdentityMetadataAdmin(ctx, userID, metadataAdmin); err != nil {
		return errs.Internal(err)
	}
	if err := s.identityManager.DeleteIdentitySessions(ctx, userID); err != nil {
		return errs.Internal(err)
	}
	return nil
}

func (s *UserStateService) ClearBan(ctx context.Context, userID string) error {
	identity, err := s.identityManager.GetIdentity(ctx, userID)
	if err != nil {
		return errs.Internal(err)
	}
	if identity == nil {
		return errs.InternalMsg("identity is required")
	}
	metadataAdmin := cloneIdentityMetadataAdmin(identity.MetadataAdmin)
	metadataAdmin["banned"] = false
	metadataAdmin["ban_reason"] = nil
	metadataAdmin["ban_expires"] = nil
	if err := s.identityManager.SetIdentityState(ctx, userID, auth.KratosStateActive); err != nil {
		return errs.Internal(err)
	}
	if err := s.identityManager.UpdateIdentityMetadataAdmin(ctx, userID, metadataAdmin); err != nil {
		return errs.Internal(err)
	}
	return nil
}

func cloneIdentityMetadataAdmin(metadata structured.Fields) structured.Fields {
	cloned := make(structured.Fields, len(metadata)+3)
	maps.Copy(cloned, metadata)
	return cloned
}
