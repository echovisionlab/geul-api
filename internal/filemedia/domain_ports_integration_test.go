//go:build integration

package filemedia

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	audiencedomain "github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// integrationPostAccess keeps FileMedia package tests at the consumer port.
// The production adapter delegates to the Post-owned locked boundary; this
// test implementation uses the test database and checker directly because the
// SQLite FileMedia fixtures do not contain the production Identity tables.
type integrationPostAccess struct {
	db      *gorm.DB
	spiceDB *auth.SpiceDBClient
}

type integrationProgramEventAccess struct {
	db *gorm.DB
}

type integrationAudienceAccess struct{}

func newIntegrationPostAccess(db *gorm.DB, spiceDB *auth.SpiceDBClient) *integrationPostAccess {
	return &integrationPostAccess{db: db, spiceDB: spiceDB}
}

func newIntegrationProgramEventAccess(db *gorm.DB) *integrationProgramEventAccess {
	return &integrationProgramEventAccess{db: db}
}

func newIntegrationAudienceAccess() *integrationAudienceAccess {
	return &integrationAudienceAccess{}
}

func (*integrationAudienceAccess) ValidateAuthenticatedSegmentIDs(
	ctx context.Context,
	tx *gorm.DB,
	segmentIDs []string,
) error {
	return audiencedomain.ValidateAuthenticatedAccessSegmentIDs(ctx, tx, segmentIDs)
}

func (*integrationAudienceAccess) AuthenticatedSegmentSummary(
	segment *model.AudienceSegment,
) (*managev1.AudienceSegmentSummary, bool) {
	return audiencedomain.AuthenticatedAccessSegmentSummary(segment)
}

func (a *integrationPostAccess) RequireView(ctx context.Context, postID string) error {
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return a.RequireLockedView(ctx, tx, postID)
	})
}

func (a *integrationPostAccess) RequireEdit(ctx context.Context, postID string) error {
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return a.RequireLockedEdit(ctx, tx, postID)
	})
}

func (a *integrationPostAccess) RequireLockedView(ctx context.Context, tx *gorm.DB, postID string) error {
	return a.requireLocked(ctx, tx, postID, policyv1.Post.View, policyv1.Post.ViewArchived, "SHARE")
}

func (a *integrationPostAccess) RequireLockedEdit(ctx context.Context, tx *gorm.DB, postID string) error {
	return a.requireLocked(ctx, tx, postID, policyv1.Post.Edit, policyv1.Post.EditArchived, "UPDATE")
}

func (a *integrationPostAccess) requireLocked(
	ctx context.Context,
	tx *gorm.DB,
	postID string,
	ordinary auth.ResourceAction,
	archived auth.ResourceAction,
	lockStrength string,
) error {
	var row struct {
		Status string `gorm:"column:status"`
	}
	result := tx.WithContext(ctx).Table("post").Clauses(clause.Locking{Strength: lockStrength}).
		Select("status").Where("id = ?", postID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return errs.NotFound("post", postID)
	}
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	action := ordinary
	if row.Status == managev1.PostStatus_POST_STATUS_ARCHIVED.String() {
		action = archived
	}
	principal := auth.GetUser(ctx)
	if principal == nil {
		return errs.NotFound("post", postID)
	}
	can, err := action(postID)
	if err != nil {
		return errs.NotFound("post", postID)
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.NotFound("post", postID)
	}
	allowed, err := a.spiceDB.Can(ctx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NotFound("post", postID)
	}
	return nil
}

func (a *integrationProgramEventAccess) RequireView(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	eventID string,
) error {
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return a.RequireLockedView(ctx, tx, spiceDB, eventID)
	})
}

func (a *integrationProgramEventAccess) RequireEdit(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	eventID string,
) error {
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return a.RequireLockedEdit(ctx, tx, spiceDB, eventID)
	})
}

func (a *integrationProgramEventAccess) RequireLockedView(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	eventID string,
) error {
	return requireLockedIntegrationDomainAccess(
		ctx,
		tx,
		spiceDB,
		"program_event",
		"program event",
		eventID,
		policyv1.ProgramEvent.View,
		policyv1.ProgramEvent.ViewArchived,
		"SHARE",
	)
}

func (a *integrationProgramEventAccess) RequireLockedEdit(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	eventID string,
) error {
	return requireLockedIntegrationDomainAccess(
		ctx,
		tx,
		spiceDB,
		"program_event",
		"program event",
		eventID,
		policyv1.ProgramEvent.Edit,
		policyv1.ProgramEvent.EditArchived,
		"UPDATE",
	)
}

func requireLockedIntegrationDomainAccess(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	table string,
	domain string,
	resourceID string,
	ordinary auth.ResourceAction,
	archived auth.ResourceAction,
	lockStrength string,
) error {
	var row struct {
		Status string `gorm:"column:status"`
	}
	result := tx.WithContext(ctx).Table(table).Clauses(clause.Locking{Strength: lockStrength}).
		Select("status").Where("id = ?", resourceID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return errs.NotFound(domain, resourceID)
	}
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	action := ordinary
	if row.Status == managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String() {
		action = archived
	}
	principal := auth.GetUser(ctx)
	if principal == nil {
		return errs.NotFound(domain, resourceID)
	}
	can, err := action(resourceID)
	if err != nil {
		return errs.NotFound(domain, resourceID)
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.NotFound(domain, resourceID)
	}
	allowed, err := spiceDB.Can(ctx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NotFound(domain, resourceID)
	}
	return nil
}
