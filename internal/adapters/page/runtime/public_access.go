package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	publicpage "github.com/echovisionlab/geul-api/internal/page/public"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type ShareLinkValidator interface {
	ValidateShareLinkForEntity(
		context.Context,
		*gorm.DB,
		string,
		string,
		managev1.ShareLinkEntityType,
		string,
	) (*model.ShareLink, error)
}

// PublicAccess adapts authenticated SpiceDB reads and shared ShareLink proof
// validation to the Page public not-found contract.
type PublicAccess struct {
	spiceDB    *auth.SpiceDBClient
	shareLinks ShareLinkValidator
}

func NewPublicAccess(spiceDB *auth.SpiceDBClient, shareLinks ShareLinkValidator) *PublicAccess {
	if spiceDB == nil || shareLinks == nil {
		panic("Page public access dependencies are required")
	}
	return &PublicAccess{spiceDB: spiceDB, shareLinks: shareLinks}
}

func (a *PublicAccess) CanViewPageDraft(ctx context.Context, pageID string) (bool, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.Banned || strings.TrimSpace(user.IdentityID.String()) == "" {
		return false, nil
	}
	can, err := policyv1.Page.View(pageID)
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, err
	}
	return a.spiceDB.Can(ctx, decision)
}

func (a *PublicAccess) RequirePageShareLinkAccess(
	ctx context.Context,
	db *gorm.DB,
	token string,
	password string,
	pageID string,
) (*model.ShareLink, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errs.NotFoundMsg("page not found")
	}
	link, err := a.shareLinks.ValidateShareLinkForEntity(
		ctx, db, token, password,
		managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE, pageID,
	)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.NotFoundMsg("page not found")
	}
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("validate page share link: %w", err))
	}
	return link, nil
}

var (
	_ publicpage.DraftAccessChecker     = (*PublicAccess)(nil)
	_ publicpage.ShareLinkAccessChecker = (*PublicAccess)(nil)
)
