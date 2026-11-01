package postpostgres

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// The functions here run a Post step inside a transaction another domain
// opened (File access, Series membership, translation delivery and
// interchange), binding the Store to it.

// freshRequire is mutation.FreshCan's check in tx, the caller's
// transaction.
func freshRequire(ctx context.Context, tx *gorm.DB, checker authz.AuthorizationDecisionChecker) mutation.Require {
	return func(can policyv1.Can) error {
		return identitystate.RequireFreshCan(ctx, tx, identitystate.PostgresFence{}, checker, can)
	}
}

// RequireLockedView locks the Post lifecycle and the current principal in
// tx before one exact view decision (post.RequireLockedView). Callers must
// keep tx open for the entire Post-owned read.
func RequireLockedView(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, postID string) error {
	return post.RequireLockedView(ctx, NewStore(tx, Ports{}), freshRequire(ctx, tx, spiceDB), postID)
}

// RequireLockedSourceLocaleEdit locks the Post lifecycle and the current
// principal in tx and requires the edit action the lifecycle selects
// (post.RequireLockedSourceLocaleEdit).
func RequireLockedSourceLocaleEdit(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, postID string) error {
	return post.RequireLockedSourceLocaleEdit(ctx, NewStore(tx, Ports{}), freshRequire(ctx, tx, spiceDB), postID)
}

// LoadTypedTranslationSourceDocument reads the Post's translation source
// through db with the content store the translation domain hands it.
func LoadTypedTranslationSourceDocument(ctx context.Context, db *gorm.DB, store *contentblock.Store, postID string) (*translation.SourceDocument, error) {
	if store == nil {
		return nil, errors.New("post translation content Block store is not configured")
	}
	return post.LoadTypedTranslationSourceDocument(ctx, NewStore(db, Ports{ContentBlocks: store}), postID)
}

// ApplyTypedTranslationCandidateWithDB delivers a provider translation in
// tx, the translation domain's transaction.
func ApplyTypedTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	metadata translation.EntryWrite,
	audit domainaudit.Appender,
) error {
	if store == nil || job == nil {
		return errors.New("typed post translation candidate and content Block store are required")
	}
	return post.ApplyTypedTranslationCandidate(ctx, NewStore(tx, Ports{ContentBlocks: store}), translationJob(job), candidate, metadata, audit)
}

// TranslationInterchange runs Post's XLIFF seam (post.TranslationInterchange)
// in the translation domain's transaction.
type TranslationInterchange struct {
	domain *post.TranslationInterchange
}

// NewTranslationInterchange returns the Post XLIFF seam appending its
// locale-content records to audit.
func NewTranslationInterchange(audit domainaudit.Appender) (*TranslationInterchange, error) {
	domain, err := post.NewTranslationInterchange(audit)
	if err != nil {
		return nil, err
	}
	return &TranslationInterchange{domain: domain}, nil
}

// ApplyCandidateWithDB applies an XLIFF target in tx with the content
// store the translation domain hands it.
func (i *TranslationInterchange) ApplyCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	entry translation.EntryWrite,
	expectedRevision *string,
) (post.TranslationInterchangeMutationResult, error) {
	if i == nil || tx == nil || store == nil || job == nil || candidate == nil {
		return post.TranslationInterchangeMutationResult{}, errors.New("post translation interchange dependencies are required")
	}
	return i.domain.ApplyCandidate(ctx, NewStore(tx, Ports{ContentBlocks: store}), translationJob(job), candidate, entry, expectedRevision)
}

func translationJob(job *model.TranslationJob) post.TranslationJob {
	return post.TranslationJob{
		EntityType: job.EntityType, EntityID: job.EntityID, SourceLocale: job.SourceLocale,
		TargetLocale: job.TargetLocale, RequestedByMemberID: job.RequestedByMemberID,
	}
}
