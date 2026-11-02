package campaignpostgres

import (
	queryutil "github.com/echovisionlab/geul-api/internal/query"
)

var campaignSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"subject":    "subject",
		"status":     "status",
		"sent_count": "sent_count",
		"sentCount":  "sent_count", // camelCase alias
		"created_at": "created_at",
		"updated_at": "updated_at",
	},
	DefaultSort: "created_at DESC, id ASC",
}
