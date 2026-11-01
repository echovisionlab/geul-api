// Package identityread is the one product reader of Kratos identities. The
// table, its bilateral Member link (identities.external_id = member.id) and
// the meaning of its lifecycle columns live here, so a Kratos schema change
// touches this package only. Writes stay with the Kratos admin API.
//
// Domain queries compose the SQL fragments below instead of naming
// kratos.identities; internal/architecture flags any other reference.
package identityread

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	table         = "kratos.identities"
	sessionsTable = "kratos.sessions"
)

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

// ActiveSQL is true when the Identity aliased alias is active and not banned
// in admin metadata, the state a principal needs to act.
func ActiveSQL(alias string) string {
	return fmt.Sprintf(`(%s.state = 'active' AND NOT COALESCE((%s.metadata_admin->>'banned')::boolean, false))`, alias, alias)
}

// LockLinked share-locks the Identity identityID linked to memberID for the
// rest of tx; it answers gorm.ErrRecordNotFound when that link is absent.
func LockLinked(ctx context.Context, tx *gorm.DB, identityID, memberID string) error {
	var identity struct {
		ID string `gorm:"column:id"`
	}
	return tx.WithContext(ctx).
		Table(table).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Select("id").
		Where("id = ?::uuid AND external_id = ?", identityID, memberID).
		Take(&identity).Error
}

// SessionIdentitySQL joins the Kratos session aliased session to its
// Identity aliased alias, for a FROM clause.
func SessionIdentitySQL(session, alias string) string {
	return fmt.Sprintf("%s AS %s JOIN %s AS %s ON %s.id = %s.identity_id",
		sessionsTable, session, table, alias, alias, session)
}

// LiveSessionSQL is true when the session aliased session is active and has
// not expired.
func LiveSessionSQL(session string) string {
	return fmt.Sprintf("%s.active = TRUE AND %s.expires_at > NOW()", session, session)
}

// ExistsSQL is true when an Identity has the id bound to its one parameter.
const ExistsSQL = `EXISTS (SELECT 1 FROM ` + table + ` WHERE id = ?::uuid)`

// Exists reports whether an Identity with identityID exists.
func Exists(ctx context.Context, db *gorm.DB, identityID string) (bool, error) {
	var exists bool
	err := db.WithContext(ctx).Raw(`SELECT `+ExistsSQL, identityID).Scan(&exists).Error
	return exists, err
}
