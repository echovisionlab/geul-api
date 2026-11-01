// Package identityread is the one product reader of Kratos identities and
// sessions. The identities table, its bilateral Member link
// (identities.external_id = member.id) and the meaning of its lifecycle
// columns live here, so a Kratos schema change touches this package only.
// Writes stay with the Kratos admin API.
//
// Domain queries compose the SQL fragments below instead of naming a Kratos
// table; internal/architecture flags any other reference.
package identityread

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

const table = "kratos.identities"

// JoinLinked returns the JOIN of the Identity linked bilaterally to the Member
// row aliased member, aliased alias. Rows without that live link drop out.
func JoinLinked(member, alias string) string {
	return "JOIN " + linked(member, alias)
}

// LeftJoinLinked is JoinLinked that keeps Members without a live link, with
// alias.id NULL.
func LeftJoinLinked(member, alias string) string {
	return "LEFT JOIN " + linked(member, alias)
}

func linked(member, alias string) string {
	return fmt.Sprintf("%s AS %s ON %s.id = %s.account_identity_id AND %s.external_id = %s.id::text",
		table, alias, alias, member, alias, member)
}

// BannedSQL is the administrator-facing ban state of the Identity aliased
// alias: banned in admin metadata, or deactivated.
func BannedSQL(alias string) string {
	return fmt.Sprintf(`(
	COALESCE((%s.metadata_admin ->> 'banned')::boolean, false)
	OR COALESCE(%s.state::text = 'inactive', false)
)`, alias, alias)
}

// ExistsSQL is true when an Identity has the id bound to its one parameter.
const ExistsSQL = `EXISTS (SELECT 1 FROM ` + table + ` WHERE id = ?::uuid)`

// Exists reports whether an Identity with identityID exists.
func Exists(ctx context.Context, db *gorm.DB, identityID string) (bool, error) {
	var exists bool
	err := db.WithContext(ctx).Raw(`SELECT `+ExistsSQL, identityID).Scan(&exists).Error
	return exists, err
}

// FirstSession reports whether the Kratos session sessionID is the first its
// Identity was issued: no other session of that Identity was created before
// or with it. An unknown session reports true.
func FirstSession(ctx context.Context, db *gorm.DB, sessionID string) (bool, error) {
	var first bool
	err := db.WithContext(ctx).Raw(`
		SELECT NOT EXISTS (
			SELECT 1
			FROM kratos.sessions AS other
			JOIN kratos.sessions AS current ON current.id = ?::uuid
			WHERE other.identity_id = current.identity_id
			  AND other.id <> current.id
			  AND other.created_at <= current.created_at
		) AS first
	`, sessionID).Scan(&first).Error
	return first, err
}
