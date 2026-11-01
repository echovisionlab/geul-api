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

// Join returns the JOIN of the Identity whose id is the column identityID,
// aliased alias, for a row that names its Identity without the Member link.
func Join(identityID, alias string) string {
	return fmt.Sprintf("JOIN %s AS %s ON %s.id = %s", table, alias, alias, identityID)
}

// EmailVerifiedSQL is true when the Identity aliased alias has verified the
// email address its traits carry.
func EmailVerifiedSQL(alias string) string {
	return fmt.Sprintf(`COALESCE(EXISTS (
	SELECT 1 FROM %s AS address
	WHERE address.identity_id = %s.id
	  AND address.via = 'email'
	  AND address.verified
	  AND lower(btrim(address.value)) = lower(btrim(%s.traits ->> 'email'))
), false)`, verifiableAddresses, alias, alias)
}

// LockedState returns the state of the Identity identityID, locking its row
// until the transaction ends; it is empty when the Identity is missing.
func LockedState(ctx context.Context, tx *gorm.DB, identityID string) (string, error) {
	var state string
	err := tx.WithContext(ctx).Raw(`SELECT state FROM `+table+` WHERE id = ?::uuid FOR UPDATE`, identityID).Scan(&state).Error
	return state, err
}

const (
	sessions            = "kratos.sessions"
	verifiableAddresses = "kratos.identity_verifiable_addresses"
)

// ActiveSessionSQL is true when the Identity whose id is the column
// identityID has a session Kratos still marks active, expired or not.
func ActiveSessionSQL(identityID string) string {
	return fmt.Sprintf(`EXISTS (
	SELECT 1 FROM %s AS session
	WHERE session.identity_id = %s
	  AND session.active = TRUE
)`, sessions, identityID)
}

// LiveSessionsSQL selects from the active, unexpired sessions of the
// Identity bound to its one parameter; a query appends further conditions
// with AND.
const LiveSessionsSQL = sessions + `
	WHERE identity_id = ?
	  AND active = TRUE
	  AND expires_at > CURRENT_TIMESTAMP`
