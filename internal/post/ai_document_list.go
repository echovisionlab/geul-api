package post

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const (
	defaultAIDocumentListLimit = 20
	maximumAIDocumentListLimit = 50
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
	ID           string
	Title        string
	Slug         *string
	SourceLocale string
	Status       string
	UpdatedAt    time.Time
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
func (s *PostService) ListAIDocuments(
	ctx context.Context,
	input AIDocumentListInput,
) (AIDocumentListResult, error) {
	if s == nil || s.db == nil || s.spiceDB == nil {
		return AIDocumentListResult{}, errs.DependencyUnavailable("Post AI document discovery")
	}

	queryText := strings.TrimSpace(input.Query)
	if len(queryText) > maximumAIDocumentQuerySize {
		return AIDocumentListResult{}, errs.InvalidArgument(
			"query",
			"must contain at most 200 characters",
		)
	}
	limit := input.Limit
	if limit == 0 {
		limit = defaultAIDocumentListLimit
	}
	if limit < 1 || limit > maximumAIDocumentListLimit {
		return AIDocumentListResult{}, errs.InvalidArgument("limit", "must be between 1 and 50")
	}
	if input.Offset < 0 {
		return AIDocumentListResult{}, errs.InvalidArgument("offset", "must be non-negative")
	}

	query, err := s.authorizedPostListQuery(ctx)
	if err != nil {
		return AIDocumentListResult{}, err
	}
	if queryText != "" {
		query = query.Where(
			"("+PostSourceTitleSQL+") ILIKE ? ESCAPE E'\\\\'",
			"%"+escapePostTitleSearch(queryText)+"%",
		)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return AIDocumentListResult{}, errs.Internal(err)
	}

	type row struct {
		ID           string           `gorm:"column:id"`
		Title        string           `gorm:"column:title"`
		Slug         *string          `gorm:"column:slug"`
		SourceLocale string           `gorm:"column:source_locale"`
		Status       model.PostStatus `gorm:"column:status"`
		UpdatedAt    time.Time        `gorm:"column:updated_at"`
	}
	var rows []row
	if err := query.
		Select(
			"post.id::text AS id",
			PostSourceTitleSQL+" AS title",
			"post.slug",
			"post.source_locale",
			"post.status",
			"post.updated_at",
		).
		Order("post.updated_at DESC, post.id ASC").
		Limit(limit).
		Offset(input.Offset).
		Find(&rows).Error; err != nil {
		return AIDocumentListResult{}, errs.Internal(err)
	}

	items := make([]AIDocumentListItem, len(rows))
	for index, item := range rows {
		items[index] = AIDocumentListItem{
			ID: item.ID, Title: item.Title, Slug: item.Slug,
			SourceLocale: item.SourceLocale, Status: string(item.Status), UpdatedAt: item.UpdatedAt,
		}
	}
	return AIDocumentListResult{
		Items: items, Total: total, Limit: limit, Offset: input.Offset,
	}, nil
}

func (s *PostService) authorizedPostListQuery(ctx context.Context) (*gorm.DB, error) {
	if s == nil || s.db == nil || s.spiceDB == nil {
		return nil, errs.DependencyUnavailable("Post list")
	}
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, errs.AuthenticationRequired()
	}
	subject, err := auth.NewAccountIdentitySubject(user.IdentityID)
	if err != nil {
		return nil, errs.AuthenticationRequired()
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return nil, errs.AuthenticationRequired()
	}
	viewPostIDs, err := lookupPostResources(ctx, s.spiceDB, actor, policyv1.Post.LookupView())
	if err != nil {
		return nil, err
	}
	archivedPostIDs, err := lookupPostResources(ctx, s.spiceDB, actor, policyv1.Post.LookupViewArchived())
	if err != nil {
		return nil, err
	}

	query := s.db.WithContext(ctx).Model(&model.Post{})
	archivedStatus := managev1.PostStatus_POST_STATUS_ARCHIVED.String()
	switch {
	case len(viewPostIDs) > 0 && len(archivedPostIDs) > 0:
		return query.Where(
			"((status <> ? AND id IN ?) OR (status = ? AND id IN ?))",
			archivedStatus,
			viewPostIDs,
			archivedStatus,
			archivedPostIDs,
		), nil
	case len(viewPostIDs) > 0:
		return query.Where("status <> ? AND id IN ?", archivedStatus, viewPostIDs), nil
	case len(archivedPostIDs) > 0:
		return query.Where("status = ? AND id IN ?", archivedStatus, archivedPostIDs), nil
	default:
		return query.Where("FALSE"), nil
	}
}

func escapePostTitleSearch(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}
