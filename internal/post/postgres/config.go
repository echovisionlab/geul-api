package postpostgres

import (
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// The source-locale title and content text of the post row aliased post,
// for filters, sorts and searches.
const (
	PostSourceTitleSQL       = "COALESCE((SELECT pt.title FROM post_translation AS pt WHERE pt.entity_id = post.id AND pt.locale = post.source_locale LIMIT 1), '')"
	PostSourceContentTextSQL = "COALESCE((SELECT string_agg(cbl.localized_data::text, ' ') FROM content_block AS cb JOIN content_block_locale AS cbl ON cbl.block_id = cb.id WHERE cb.document_id = post.content_document_id AND cbl.locale = post.source_locale), '')"
)

var PostAdminFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:          queryutil.TypeText,
			AllowedOps:    queryutil.SearchOps,
			SearchColumns: []string{PostSourceTitleSQL},
		},
		"status": {
			Column:     "status",
			Type:       queryutil.TypeEnum,
			AllowedOps: queryutil.EnumOps,
			EnumValues: []string{
				string(model.PostStatusDraft),
				string(model.PostStatusScheduled),
				string(model.PostStatusPublished),
				string(model.PostStatusArchived),
			},
		},
		"series_id": {
			Column: "series_id", Type: queryutil.TypeID, AllowedOps: queryutil.IDOps, IsFK: true,
		},
		"map_place_id": {
			Column: "map_place_id", Type: queryutil.TypeID,
			AllowedOps: []commonv1.FilterOp{
				commonv1.FilterOp_FILTER_OP_EQ,
				commonv1.FilterOp_FILTER_OP_NEQ,
				commonv1.FilterOp_FILTER_OP_IN,
				commonv1.FilterOp_FILTER_OP_NOT_IN,
				commonv1.FilterOp_FILTER_OP_IS_NULL,
				commonv1.FilterOp_FILTER_OP_IS_NOT_NULL,
			},
			IsFK: true,
		},
		"published_at": {
			Column: "published_at", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps,
		},
	},
}

// postSortConfig defines allowed sort fields for posts
var postSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"created_at":   "created_at",
		"updated_at":   "updated_at",
		"published_at": "published_at",
		"title":        PostSourceTitleSQL,
		"status":       "status",
	},
	DefaultSort: "published_at DESC NULLS LAST, created_at DESC",
}

// applyPostRelationFilters applies the filters that select Posts by a
// relation table, which the filter configuration does not describe, and
// returns the others.
func applyPostRelationFilters(stored *gorm.DB, filters []*commonv1.FilterSpec, byAuthor bool) (*gorm.DB, []*commonv1.FilterSpec) {
	relations := map[string]string{
		"category_id": "id IN (SELECT post_id FROM post_category WHERE category_id %s)",
		"tag_id":      "id IN (SELECT post_id FROM post_tag WHERE tag_id %s)",
	}
	var remaining []*commonv1.FilterSpec
	for _, filter := range filters {
		if filter == nil {
			continue
		}
		condition, related := relations[filter.GetField()]
		switch {
		case related && filter.GetOp() == commonv1.FilterOp_FILTER_OP_EQ:
			stored = stored.Where(fmt.Sprintf(condition, "= ?"), filter.GetValue())
		case related && filter.GetOp() == commonv1.FilterOp_FILTER_OP_IN:
			stored = stored.Where(fmt.Sprintf(condition, "IN ?"), filter.GetValues())
		case related:
		case byAuthor && filter.GetField() == "author_id":
			// Authorship is a durable domain relation, independent of the
			// current SpiceDB authorization grant.
			if authorIDs := postFilterValues(filter); len(authorIDs) > 0 {
				stored = stored.Where("id IN (SELECT post_id FROM post_author WHERE member_id IN ?)", authorIDs)
			}
		default:
			remaining = append(remaining, filter)
		}
	}
	return stored, remaining
}

func postFilterValues(filter *commonv1.FilterSpec) []string {
	switch filter.GetOp() {
	case commonv1.FilterOp_FILTER_OP_EQ:
		return []string{filter.GetValue()}
	case commonv1.FilterOp_FILTER_OP_IN:
		return filter.GetValues()
	default:
		return nil
	}
}

func escapePostTitleSearch(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func isUniqueViolation(err error) bool {
	return dberrors.IsUniqueViolation(err)
}
