package ogadapter

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Authorization keeps identity and SpiceDB policy decisions outside the OG
// lifecycle while satisfying the OG admin service's authorization port.
type Authorization struct {
	spiceDB *auth.SpiceDBClient
}

func NewAuthorization(spiceDB *auth.SpiceDBClient) Authorization {
	if spiceDB == nil {
		panic("SpiceDB client is required")
	}
	return Authorization{spiceDB: spiceDB}
}

func (a Authorization) RequireAuthenticated(ctx context.Context) error {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated {
		return errs.AuthenticationRequired()
	}
	if user.Banned {
		return errs.PermissionDenied("account is banned")
	}
	return nil
}

func (a Authorization) RequireAdmin(ctx context.Context) error {
	user := auth.GetUser(ctx)
	if user == nil || user.Banned {
		return errs.AdminRequired()
	}
	if !user.Authenticated {
		return errs.NotAuthenticated()
	}
	can, err := policyv1.Platform.IsAdmin()
	if err != nil {
		return errs.AdminRequired()
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
		return errs.AdminRequired()
	}
	return nil
}

func (a Authorization) AuthorizeEntity(ctx context.Context, entityType string, entityID string, requireEdit bool) error {
	if err := a.RequireAuthenticated(ctx); err != nil {
		return err
	}
	adminCan, err := policyv1.Platform.IsAdmin()
	if err != nil {
		return errs.AdminRequired()
	}
	adminDecision, err := auth.AuthorizationDecision(ctx, adminCan)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	isAdmin, err := a.spiceDB.Can(ctx, adminDecision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if isAdmin {
		return nil
	}
	can, err := entityCan(entityType, entityID, requireEdit)
	if err != nil {
		return errs.AdminRequired()
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
		return errs.NoPermission("access", can.Resource().Type())
	}
	return nil
}

func entityCan(entityType, entityID string, requireEdit bool) (policyv1.Can, error) {
	type canPair struct {
		view func(string) (policyv1.Can, error)
		edit func(string) (policyv1.Can, error)
	}
	var pair canPair
	switch entityType {
	case "post":
		pair = canPair{view: policyv1.Post.View, edit: policyv1.Post.Edit}
	case "page":
		pair = canPair{view: policyv1.Page.View, edit: policyv1.Page.Edit}
	case "work":
		pair = canPair{view: policyv1.Work.View, edit: policyv1.Work.Edit}
	case "series":
		pair = canPair{view: policyv1.PostSeries.View, edit: policyv1.PostSeries.Edit}
	case "form":
		pair = canPair{view: policyv1.Form.View, edit: policyv1.Form.Edit}
	case "campaign":
		pair = canPair{view: policyv1.Campaign.View, edit: policyv1.Campaign.Edit}
	case "email_template":
		pair = canPair{view: policyv1.EmailTemplate.View, edit: policyv1.EmailTemplate.Edit}
	default:
		return policyv1.Can{}, fmt.Errorf("unsupported OG entity type %q", entityType)
	}
	if requireEdit {
		return pair.edit(entityID)
	}
	return pair.view(entityID)
}
