package filemedia

import (
	"context"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	audiencedomain "github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/model"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/programevent"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type PostAccess struct {
	db      *gorm.DB
	spiceDB *auth.SpiceDBClient
}

func NewPostAccess(db *gorm.DB, spiceDB *auth.SpiceDBClient) *PostAccess {
	return &PostAccess{db: db, spiceDB: spiceDB}
}

func (a *PostAccess) RequireView(ctx context.Context, postID string) error {
	if a == nil || a.db == nil {
		return errs.DependencyUnavailable("Post access")
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return a.RequireLockedView(ctx, tx, postID)
	})
}

func (a *PostAccess) RequireEdit(ctx context.Context, postID string) error {
	if a == nil || a.db == nil {
		return errs.DependencyUnavailable("Post access")
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return a.RequireLockedEdit(ctx, tx, postID)
	})
}

func (a *PostAccess) RequireLockedView(ctx context.Context, tx *gorm.DB, postID string) error {
	if a == nil || tx == nil || a.spiceDB == nil {
		return errs.DependencyUnavailable("Post access")
	}
	return maskPrivatePostAccess(postdomain.RequireLockedView(ctx, tx, a.spiceDB, postID), postID)
}

func (a *PostAccess) RequireLockedEdit(ctx context.Context, tx *gorm.DB, postID string) error {
	if a == nil || tx == nil || a.spiceDB == nil {
		return errs.DependencyUnavailable("Post access")
	}
	return maskPrivatePostAccess(postdomain.RequireLockedSourceLocaleEdit(ctx, tx, a.spiceDB, postID), postID)
}

func maskPrivatePostAccess(err error, postID string) error {
	return maskPrivateDomainAccess(err, "post", postID)
}

func maskPrivateDomainAccess(err error, domain, resourceID string) error {
	if err != nil {
		switch connect.CodeOf(err) {
		case connect.CodeUnauthenticated, connect.CodePermissionDenied:
			return errs.NotFound(domain, resourceID)
		}
	}
	return err
}

type WorkAttachment struct{ db *gorm.DB }

func NewWorkAttachment(db *gorm.DB) *WorkAttachment { return &WorkAttachment{db: db} }

func (a *WorkAttachment) RequireExists(ctx context.Context, workID string) error {
	return workdomain.RequireExists(ctx, a.db, workID)
}

type ProgramEventAttachment struct{ db *gorm.DB }

func NewProgramEventAttachment(db *gorm.DB) *ProgramEventAttachment {
	return &ProgramEventAttachment{db: db}
}

func (a *ProgramEventAttachment) RequireView(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	eventID string,
) error {
	if a == nil || a.db == nil {
		return errs.DependencyUnavailable("Program Event access")
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return a.RequireLockedView(ctx, tx, spiceDB, eventID)
	})
}

func (a *ProgramEventAttachment) RequireEdit(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	eventID string,
) error {
	if a == nil || a.db == nil {
		return errs.DependencyUnavailable("Program Event access")
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return a.RequireLockedEdit(ctx, tx, spiceDB, eventID)
	})
}

func (a *ProgramEventAttachment) RequireLockedView(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	eventID string,
) error {
	if a == nil || tx == nil || spiceDB == nil {
		return errs.DependencyUnavailable("Program Event access")
	}
	return maskPrivateDomainAccess(
		programevent.RequireLockedView(ctx, tx, spiceDB, eventID),
		"program event",
		eventID,
	)
}

func (a *ProgramEventAttachment) RequireLockedEdit(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	eventID string,
) error {
	if a == nil || tx == nil || spiceDB == nil {
		return errs.DependencyUnavailable("Program Event access")
	}
	return maskPrivateDomainAccess(
		programevent.RequireLockedSourceLocaleEdit(ctx, tx, spiceDB, eventID),
		"program event",
		eventID,
	)
}

type AudienceAccess struct{}

func NewAudienceAccess() *AudienceAccess { return &AudienceAccess{} }

func (*AudienceAccess) ValidateAuthenticatedSegmentIDs(
	ctx context.Context,
	tx *gorm.DB,
	segmentIDs []string,
) error {
	return audiencedomain.ValidateAuthenticatedAccessSegmentIDs(ctx, tx, segmentIDs)
}

func (*AudienceAccess) AuthenticatedSegmentSummary(
	segment *model.AudienceSegment,
) (*managev1.AudienceSegmentSummary, bool) {
	return audiencedomain.AuthenticatedAccessSegmentSummary(segment)
}

type MemberSummaries struct {
	db        *gorm.DB
	cdnDomain string
}

func NewMemberSummaries(db *gorm.DB, cdnDomain string) *MemberSummaries {
	return &MemberSummaries{db: db, cdnDomain: cdnDomain}
}

func (a *MemberSummaries) Load(
	ctx context.Context,
	memberIDs []string,
) (map[string]*commonv1.MemberSummary, error) {
	return memberdomain.LoadSummaries(ctx, a.db, a.cdnDomain, memberIDs)
}
