package authz

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

// SpiceDBResourceChecker is the final object-authorization boundary. All
// resource decisions are delegated to the typed SpiceDB adapter; PostgreSQL is
// consulted only for existence hiding.
type SpiceDBResourceChecker struct {
	spicedb   AuthorizationDecisionChecker
	db        *gorm.DB
	tableName string
}

func NewSpiceDBResourceChecker(client *auth.SpiceDBClient, db *gorm.DB, tableName string) *SpiceDBResourceChecker {
	return newSpiceDBResourceChecker(client, db, tableName)
}

func newSpiceDBResourceChecker(client AuthorizationDecisionChecker, db *gorm.DB, tableName string) *SpiceDBResourceChecker {
	if client == nil {
		panic("SpiceDB client is required")
	}
	if strings.TrimSpace(tableName) == "" {
		panic("resource table name is required")
	}
	return &SpiceDBResourceChecker{spicedb: client, db: db, tableName: tableName}
}

func (c *SpiceDBResourceChecker) MustAuthenticate(ctx context.Context) (*auth.UserInfo, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.MemberID == "" {
		return nil, errs.AuthenticationRequired()
	}
	return user, nil
}

func (c *SpiceDBResourceChecker) CheckView(ctx context.Context, can policyv1.Can) error {
	err := c.Check(ctx, can)
	if err == nil {
		return nil
	}
	switch connect.CodeOf(err) {
	case connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeNotFound:
		if can.Valid() {
			return errs.NotFoundMsg(fmt.Sprintf("%s not found", can.Resource().Type()))
		}
		return errs.NotFoundMsg("resource not found")
	default:
		return err
	}
}

// Check authorizes one complete generated Can descriptor for the authenticated
// account identity and request delegation.
func (c *SpiceDBResourceChecker) Check(
	ctx context.Context,
	can policyv1.Can,
) error {
	if _, err := c.MustAuthenticate(ctx); err != nil {
		return err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	ok, err := c.spicedb.Can(ctx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !ok {
		return errs.NoPermission(can.Action().Name(), can.Resource().Type())
	}
	return nil
}

// CheckOrNotFound preserves permission denial for an existing
// resource and masks a missing resource as not found. It is the only path that
// consults PostgreSQL after an exact SpiceDB denial.
func (c *SpiceDBResourceChecker) CheckOrNotFound(
	ctx context.Context,
	can policyv1.Can,
) error {
	err := c.Check(ctx, can)
	if err == nil || c.db == nil {
		return err
	}
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		return err
	}
	if !can.Valid() {
		return errs.NotFoundMsg("resource not found")
	}
	resource := can.Resource()
	if exists, dbErr := c.resourceExists(ctx, resource.ID()); dbErr != nil {
		return errs.Internal(dbErr)
	} else if !exists {
		return errs.NotFound(resource.Type(), resource.ID())
	}
	return err
}

func (c *SpiceDBResourceChecker) resourceExists(ctx context.Context, resourceID string) (bool, error) {
	var count int64
	if err := c.db.WithContext(ctx).Table(c.tableName).Where("id = ?", resourceID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
