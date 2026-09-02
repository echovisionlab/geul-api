// Package securityaccess records access to personal data.
package securityaccess

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Appender persists security-access records.
//
// The consuming domain owns the decision about when an access record is
// required; Recorder owns the common request, actor, and record-envelope
// rules for personal-data access.
type Appender interface {
	AppendSecurityAccess(context.Context, sharedtelemetry.SecurityAccessRecord) error
}

// Recorder appends personal-data access records for the supported read scopes.
type Recorder struct {
	writer Appender
	now    func() time.Time
}

// New constructs a personal-data access recorder.
func New(writer Appender) *Recorder {
	if writer == nil {
		panic("personal data access security writer is required")
	}
	return &Recorder{
		writer: writer,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// AppendMemberCollection records an administrative read of the member
// collection.
func (recorder *Recorder) AppendMemberCollection(ctx context.Context) error {
	return recorder.append(ctx, sharedtelemetry.NewMemberCollectionAccessedRecord)
}

// AppendMember records an administrative read of one member.
func (recorder *Recorder) AppendMember(ctx context.Context, memberID string) error {
	return recorder.append(ctx, func(
		metadata sharedtelemetry.SecurityAccessMetadata,
	) (sharedtelemetry.SecurityAccessRecord, error) {
		return sharedtelemetry.NewMemberAccessedRecord(metadata, memberID)
	})
}

// AppendCampaignRecipients records an administrative read of one campaign's
// recipients.
func (recorder *Recorder) AppendCampaignRecipients(ctx context.Context, campaignID string) error {
	return recorder.append(ctx, func(
		metadata sharedtelemetry.SecurityAccessMetadata,
	) (sharedtelemetry.SecurityAccessRecord, error) {
		return sharedtelemetry.NewCampaignRecipientsAccessedRecord(metadata, campaignID)
	})
}

// AppendFormSubmissions records an administrative read of a form's
// submissions.
func (recorder *Recorder) AppendFormSubmissions(ctx context.Context, formID string) error {
	return recorder.append(ctx, func(
		metadata sharedtelemetry.SecurityAccessMetadata,
	) (sharedtelemetry.SecurityAccessRecord, error) {
		return sharedtelemetry.NewFormSubmissionsAccessedRecord(metadata, formID)
	})
}

// AppendFormSubmission records an administrative read of one form
// submission.
func (recorder *Recorder) AppendFormSubmission(ctx context.Context, submissionID string) error {
	return recorder.append(ctx, func(
		metadata sharedtelemetry.SecurityAccessMetadata,
	) (sharedtelemetry.SecurityAccessRecord, error) {
		return sharedtelemetry.NewFormSubmissionAccessedRecord(metadata, submissionID)
	})
}

func (recorder *Recorder) append(
	ctx context.Context,
	build func(sharedtelemetry.SecurityAccessMetadata) (sharedtelemetry.SecurityAccessRecord, error),
) error {
	requestContext, ok := apitelemetry.RequestContextFrom(ctx)
	if !ok {
		apitelemetry.ReportSecurityAccessAppendFailure(
			ctx,
			sharedtelemetry.SecurityPersonalDataAccessed,
			sharedtelemetry.AuditAppendFailureRequestContextMissing,
		)
		return errors.New("personal data access requires request context and source IP")
	}
	actor, err := sharedtelemetry.ActorForRecord(requestContext.Actor)
	if err != nil || actor.Kind != sharedtelemetry.ActorKindMember {
		apitelemetry.ReportSecurityAccessAppendFailure(
			ctx,
			sharedtelemetry.SecurityPersonalDataAccessed,
			sharedtelemetry.AuditAppendFailureActorInvalid,
		)
		return errors.New("personal data access requires a member actor")
	}
	return apitelemetry.BuildAndAppendSecurityAccess(
		ctx,
		recorder.writer,
		sharedtelemetry.SecurityPersonalDataAccessed,
		actor,
		recorder.now(),
		build,
	)
}

// Unavailable returns the transport error used when a required access record
// cannot be persisted.
func Unavailable() *connect.Error {
	return connect.NewError(
		connect.CodeUnavailable,
		errors.New("personal data access record is temporarily unavailable"),
	)
}
