package post

import (
	"context"
	"slices"
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
	scope.ArchivedIDs = viewableArchivedIDs(scope)
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

// viewableArchivedIDs keeps the archived Posts the caller may also view while
// they are not archived: an AI document list and read name the caller's own Posts,
// not every archived Post an Author may see elsewhere.
func viewableArchivedIDs(scope ViewScope) []string {
	viewable := make(map[string]struct{}, len(scope.ViewIDs))
	for _, id := range scope.ViewIDs {
		viewable[id] = struct{}{}
	}
	return slices.DeleteFunc(slices.Clone(scope.ArchivedIDs), func(id string) bool {
		_, found := viewable[id]
		return !found
	})
}

// OwnedDocumentIDs is the Posts the caller writes or collaborates on. With the
// Author role they stay the caller's once archived.
func (s *Service) OwnedDocumentIDs(ctx context.Context) ([]string, error) {
	actor, err := currentPostActor(ctx)
	if err != nil {
		return nil, err
	}
	return lookupPostResources(ctx, s.spiceDB, actor, policyv1.Post.LookupView())
}

func currentPostActor(ctx context.Context) (policyv1.Actor, error) {
	user := auth.GetUser(ctx)
	if user == nil {
		return policyv1.Actor{}, domainerr.AuthenticationRequired()
	}
	subject, err := auth.NewAccountIdentitySubject(user.IdentityID)
	if err != nil {
		return policyv1.Actor{}, domainerr.AuthenticationRequired().WithCause(err)
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return policyv1.Actor{}, domainerr.AuthenticationRequired().WithCause(err)
	}
	return actor, nil
}

// authorizedPostScope is the Posts the caller may view: those it may view
// while not archived, and those it may view archived.
func (s *Service) authorizedPostScope(ctx context.Context) (ViewScope, error) {
	if s == nil || s.units == nil || s.spiceDB == nil {
		return ViewScope{}, domainerr.DependencyUnavailable("Post list")
	}
	actor, err := currentPostActor(ctx)
	if err != nil {
		return ViewScope{}, err
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
