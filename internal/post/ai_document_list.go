package post

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const (
	defaultAIDocumentListLimit = 20
	maximumAIDocumentListLimit = 100
	maximumAIDocumentQuerySize = 200
)

// AIDocumentListInput is the Post-owned discovery request consumed by the MCP
// adapter. It lists only Posts the current authenticated account may view.
type AIDocumentListInput struct {
	Query  string
	Limit  int
	Offset int
}

// AIDocumentListItem contains the minimum Post identity and display metadata
// needed to select a document for a later DCDP open call.
type AIDocumentListItem struct {
	ID                    string
	Title                 string
	Slug                  *string
	SourceLocale          string
	Status                string
	UpdatedAt             time.Time
	ConfigurationRevision string
}

// AIDocumentListResult is one stable offset page of authorized Post results.
type AIDocumentListResult struct {
	Items  []AIDocumentListItem
	Total  int64
	Limit  int
	Offset int
}

// ListAIDocuments applies the same Post view authority as ListMyPosts while
// returning only bounded discovery metadata. The returned ID is the exact
// document reference accepted by DCDP; a slug is display metadata only.
func (s *Service) ListAIDocuments(
	ctx context.Context,
	input AIDocumentListInput,
) (AIDocumentListResult, error) {
	if s == nil || s.units == nil || s.spiceDB == nil {
		return AIDocumentListResult{}, domainerr.DependencyUnavailable("Post AI document discovery")
	}

	queryText := strings.TrimSpace(input.Query)
	if len(queryText) > maximumAIDocumentQuerySize {
		return AIDocumentListResult{}, domainerr.InvalidField(
			"query",
			"must contain at most 200 characters",
		)
	}
	limit := input.Limit
	if limit == 0 {
		limit = defaultAIDocumentListLimit
	}
	if limit < 1 || limit > maximumAIDocumentListLimit {
		return AIDocumentListResult{}, domainerr.InvalidField("limit", "must be between 1 and 100")
	}
	if input.Offset < 0 {
		return AIDocumentListResult{}, domainerr.InvalidField("offset", "must be non-negative")
	}

	scope, err := s.authorizedPostScope(ctx)
	if err != nil {
		return AIDocumentListResult{}, err
	}
	var items []AIDocumentListItem
	var total int64
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		items, total, err = store.ListAIDocuments(ctx, AIDocumentQuery{Scope: scope, Search: queryText, Limit: limit, Offset: input.Offset})
		return err
	}); err != nil {
		return AIDocumentListResult{}, err
	}
	return AIDocumentListResult{
		Items: items, Total: total, Limit: limit, Offset: input.Offset,
	}, nil
}

// authorizedPostScope is the Posts the caller may view: those it may view
// while not archived, and those it may view archived.
func (s *Service) authorizedPostScope(ctx context.Context) (ViewScope, error) {
	if s == nil || s.units == nil || s.spiceDB == nil {
		return ViewScope{}, domainerr.DependencyUnavailable("Post list")
	}
	user := auth.GetUser(ctx)
	if user == nil {
		return ViewScope{}, domainerr.AuthenticationRequired()
	}
	subject, err := auth.NewAccountIdentitySubject(user.IdentityID)
	if err != nil {
		return ViewScope{}, domainerr.AuthenticationRequired().WithCause(err)
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return ViewScope{}, domainerr.AuthenticationRequired().WithCause(err)
	}
	viewPostIDs, err := lookupPostResources(ctx, s.spiceDB, actor, policyv1.Post.LookupView())
	if err != nil {
		return ViewScope{}, err
	}
	archivedPostIDs, err := lookupPostResources(ctx, s.spiceDB, actor, policyv1.Post.LookupViewArchived())
	if err != nil {
		return ViewScope{}, err
	}
	return ViewScope{ViewIDs: viewPostIDs, ArchivedIDs: archivedPostIDs}, nil
}
