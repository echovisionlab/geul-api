package filemedia

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

type contentBlockFileReuseAuthorizer struct {
	spiceDB *auth.SpiceDBClient
}

// NewContentBlockFileReuseAuthorizer returns the fail-closed File library
// authority used by every Content Block domain Store.
func NewContentBlockFileReuseAuthorizer(
	spiceDB *auth.SpiceDBClient,
) contentblock.FileReuseAuthorizer {
	return &contentBlockFileReuseAuthorizer{spiceDB: spiceDB}
}

func (a *contentBlockFileReuseAuthorizer) AuthorizeFileReuse(
	ctx context.Context,
	tx *gorm.DB,
	document contentblock.Document,
	_ contentblock.FullBlock,
	_ contentblock.FileReference,
	file contentblock.File,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.MemberID == "" {
		return errs.AuthenticationRequired()
	}
	if principal.Banned {
		return errs.AccountBanned()
	}

	var alreadyInDocument bool
	if err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM content_block_attachment AS cbf
			JOIN content_block AS cb ON cb.id = cbf.block_id
			WHERE cbf.selector_kind = 'active' AND cbf.file_id = ? AND cb.document_id = ?
		)`, file.ID, document.ID).Scan(&alreadyInDocument).Error; err != nil {
		return fmt.Errorf("check document File reuse: %w", err)
	}
	if alreadyInDocument {
		return nil
	}

	var uploader struct {
		MemberID *string `gorm:"column:uploaded_by_member_id"`
	}
	if err := tx.WithContext(ctx).
		Table("file").
		Select("uploaded_by_member_id").
		Where("id = ?", file.ID).
		Scan(&uploader).Error; err != nil {
		return fmt.Errorf("read File uploader: %w", err)
	}
	if uploader.MemberID != nil && *uploader.MemberID == principal.MemberID.String() {
		return nil
	}
	if a.spiceDB == nil {
		return fmt.Errorf("SpiceDB File reuse authorization is not configured")
	}
	can, err := policyv1.File.List()
	if err != nil {
		return err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := a.spiceDB.Can(ctx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NoPermission("access", "platform")
	}
	return nil
}
