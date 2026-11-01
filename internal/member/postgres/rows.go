package memberpostgres

import (
	"slices"

	"github.com/echovisionlab/geul-api/internal/identityread"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
)

func memberFromRow(row model.Member) member.Member {
	return member.Member{
		ID: row.ID, AccountIdentityID: row.AccountIdentityID, Nickname: row.Nickname, Onboarded: row.Onboarded,
		PrimaryEmail: row.PrimaryEmail, AvailableEmails: slices.Clone([]string(row.AvailableEmails)),
		Bio: row.Bio, Website: row.Website, SocialLinks: row.SocialLinks, PreferredLocale: row.PreferredLocale,
		DeletedAt: row.DeletedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func membersFromRows(rows []model.Member) []member.Member {
	members := make([]member.Member, len(rows))
	for i, row := range rows {
		members[i] = memberFromRow(row)
	}
	return members
}

func rowFromMember(value member.Member) model.Member {
	return model.Member{
		ID: value.ID, AccountIdentityID: value.AccountIdentityID, Nickname: value.Nickname, Onboarded: value.Onboarded,
		PrimaryEmail: value.PrimaryEmail, AvailableEmails: value.AvailableEmails,
		Bio: value.Bio, Website: value.Website, SocialLinks: value.SocialLinks, PreferredLocale: value.PreferredLocale,
		DeletedAt: value.DeletedAt, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

const memberAdminPendingDeletionSQL = `EXISTS (
	SELECT 1
	FROM user_deletion_request AS deletion
	WHERE deletion.member_id = m.id
	  AND deletion.lifecycle_state IN ('scheduled', 'recovery_confirmation_pending')
	  AND deletion.scheduled_at IS NOT NULL
)`

var memberAdminBannedSQL = identityread.BannedSQL("i")

var memberAdminStatusSQL = `(CASE
	WHEN m.deleted_at IS NOT NULL OR i.id IS NULL THEN '` + string(member.AdminStatusDeleted) + `'
	WHEN ` + memberAdminPendingDeletionSQL + ` THEN '` + string(member.AdminStatusPendingDeletion) + `'
	WHEN ` + memberAdminBannedSQL + ` THEN '` + string(member.AdminStatusBanned) + `'
	ELSE '` + string(member.AdminStatusActive) + `'
END)`

var memberAdminFilterConfig = queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search": {
		Type:          queryutil.TypeText,
		AllowedOps:    queryutil.SearchOps,
		SearchColumns: []string{"m.nickname", "COALESCE(m.primary_email, '')"},
	},
	"id": {Column: "m.id", Type: queryutil.TypeID, AllowedOps: queryutil.IDOps},
	"nickname": {
		Column: "m.nickname", Type: queryutil.TypeText, AllowedOps: queryutil.TextOps,
	},
	"email": {
		Column: "m.primary_email", Type: queryutil.TypeText, AllowedOps: queryutil.TextOps,
	},
	"banned": {
		Column: memberAdminBannedSQL, Type: queryutil.TypeBool, AllowedOps: queryutil.BoolOps,
	},
	"status": {
		Column:     memberAdminStatusSQL,
		Type:       queryutil.TypeEnum,
		AllowedOps: queryutil.EnumOps,
		EnumValues: adminStatusValues(),
	},
	"newsletter_subscribed": {
		Column: "(ns.identity_id IS NOT NULL)", Type: queryutil.TypeBool, AllowedOps: queryutil.BoolOps,
	},
	"created_at": {
		Column: "m.created_at", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps,
	},
}}

func adminStatusValues() []string {
	values := make([]string, len(member.AdminStatuses))
	for i, status := range member.AdminStatuses {
		values[i] = string(status)
	}
	return values
}

var memberAdminSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"nickname":              "m.nickname",
		"email":                 "m.primary_email",
		"newsletter_subscribed": "(ns.identity_id IS NOT NULL)",
		"created_at":            "m.created_at",
	},
	DefaultSort: "m.created_at DESC, m.id DESC",
}

var memberTagAdminFilterConfig = queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search": {
		Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{"t.name"},
	},
	"name": {
		Column: "t.name", Type: queryutil.TypeText, AllowedOps: queryutil.TextOps,
	},
	"created_at": {
		Column: "t.created_at", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps,
	},
}}

var memberTagAdminSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "t.name",
		"user_count": "COUNT(m.member_id)",
		"created_at": "t.created_at",
	},
	DefaultSort: "t.name ASC, t.id ASC",
}
