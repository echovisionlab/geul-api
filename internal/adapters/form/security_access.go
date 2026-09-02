package form

import (
	"context"

	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
)

type SecurityAccess struct {
	recorder *securityaccess.Recorder
}

func NewSecurityAccess(writer securityaccess.Appender) *SecurityAccess {
	return &SecurityAccess{recorder: securityaccess.New(writer)}
}
func (a *SecurityAccess) AppendFormSubmissions(ctx context.Context, formID string) error {
	return a.recorder.AppendFormSubmissions(ctx, formID)
}
func (a *SecurityAccess) AppendFormSubmission(ctx context.Context, submissionID string) error {
	return a.recorder.AppendFormSubmission(ctx, submissionID)
}

var _ formdomain.SecurityAccess = (*SecurityAccess)(nil)
