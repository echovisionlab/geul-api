package series

import (
	"context"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
)

// PostAccess adapts Post-owned source projection and edit authorization to
// Series membership mutations.
type PostAccess struct{}

func (PostAccess) PostSourceTitleSQL() string { return postdomain.PostSourceTitleSQL }

func (PostAccess) RequireLockedEdit(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	postID string,
) error {
	// The Series aggregate independently checks its exact manage action. This
	// adapter owns only the Post decision and delegates lifecycle selection to
	// Post: edit for ordinary Posts, edit_archived for archived Posts.
	err := postdomain.RequireLockedSourceLocaleEdit(ctx, tx, spiceDB, postID)
	if err == nil {
		return nil
	}
	// Series membership is a private mutation surface. Do not reveal whether a
	// Post exists to a principal that cannot perform the selected Post action.
	switch connect.CodeOf(err) {
	case connect.CodeUnauthenticated, connect.CodePermissionDenied:
		return errs.NotFound("post", postID)
	default:
		return err
	}
}
