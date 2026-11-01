package post

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// TranslationInterchangeMutationResult is the authoritative opaque Post target
// revision produced by one accepted XLIFF mutation. It binds the current
// shared document revision and exact target locale updated_at; it is not
// translation freshness or history.
type TranslationInterchangeMutationResult struct {
	Revision string
	Changed  bool
}

// TranslationInterchange closes Post's locale-content mutation and Audit
// dependencies behind one owning-domain seam.
type TranslationInterchange struct {
	audit domainaudit.Appender
}

// NewTranslationInterchange returns the Post XLIFF seam appending its
// locale-content records to audit.
func NewTranslationInterchange(audit domainaudit.Appender) (*TranslationInterchange, error) {
	if audit == nil {
		return nil, errors.New("post translation interchange dependencies are required: Audit")
	}
	return &TranslationInterchange{audit: audit}, nil
}

// ApplyCandidate keeps XLIFF target replacement on Post's existing typed
// translation write path, then appends the exact target-locale Audit in the
// caller-owned transaction, which store is bound to. Authorization and root
// locking are performed by the Translation application before this seam.
func (i *TranslationInterchange) ApplyCandidate(
	ctx context.Context,
	store Store,
	job TranslationJob,
	candidate *translation.Candidate,
	entry translation.EntryWrite,
	expectedRevision *string,
) (TranslationInterchangeMutationResult, error) {
	if i == nil || store == nil || candidate == nil {
		return TranslationInterchangeMutationResult{}, errors.New("post translation interchange dependencies are required")
	}
	if job.EntityType != "post" || strings.TrimSpace(job.EntityID) == "" ||
		strings.TrimSpace(job.SourceLocale) == "" || strings.TrimSpace(job.TargetLocale) == "" {
		return TranslationInterchangeMutationResult{}, errors.New("post translation interchange identity is required")
	}
	if localization.NormalizeExactSupportedLocale(job.SourceLocale) == nil ||
		localization.NormalizeExactSupportedLocale(job.TargetLocale) == nil {
		return TranslationInterchangeMutationResult{}, domainerr.InvalidField(
			"locale", "source and target must be exact canonical locales",
		)
	}
	if strings.TrimSpace(job.RequestedByMemberID) == "" {
		return TranslationInterchangeMutationResult{}, domainerr.Precondition("Post translation interchange requires requester Member attribution")
	}
	documentID, err := loadPostContentDocumentID(ctx, store, job.EntityID)
	if err != nil {
		return TranslationInterchangeMutationResult{}, err
	}
	before, err := store.LockedContentSnapshot(ctx, documentID, job.SourceLocale)
	if err != nil {
		return TranslationInterchangeMutationResult{}, err
	}
	targetCount, err := store.CountLocale(ctx, job.EntityID, job.TargetLocale)
	if err != nil {
		return TranslationInterchangeMutationResult{}, err
	}
	targetPreviouslyExists := targetCount == 1
	if err := validatePostTranslationCandidate(
		candidate, postTranslationCandidateAllowSparseInterchange,
	); err != nil {
		return TranslationInterchangeMutationResult{}, err
	}
	if candidate.ContentDocumentRevision != before.Document.Revision.String() {
		return TranslationInterchangeMutationResult{}, domainerr.Precondition(
			"Post shared document changed while importing the target locale",
		)
	}
	storage, err := postInterchangeTargetStorage(candidate, before.Document.Revision.String())
	if err != nil {
		return TranslationInterchangeMutationResult{}, err
	}
	mutation, err := applyPostTargetLocaleMutation(ctx, store, postTargetLocaleMutationInput{
		PostID: job.EntityID, Locale: job.TargetLocale,
		ExpectedDocumentRevision: before.Document.Revision,
		ExpectedTargetRevision:   expectedRevision,
		Storage:                  storage,
		Policy:                   targetlocale.Policy{AllowCreate: true, SeedSource: true, AllowLocaleDeletes: true},
		SetTitle:                 true,
		Title:                    candidate.Title,
		SetSummary:               true,
		Summary:                  candidate.Summary,
		Now:                      entry.Now,
		Fence:                    postSystemTranslationDocumentFence(job.EntityID),
	})
	if err != nil {
		var conflict *translation.TargetRevisionConflict
		if errors.As(err, &conflict) {
			return TranslationInterchangeMutationResult{}, domainerr.Precondition(err.Error())
		}
		return TranslationInterchangeMutationResult{}, err
	}
	result := TranslationInterchangeMutationResult{
		Revision: mutation.TargetRevision,
		Changed:  mutation.Changed,
	}
	if !result.Changed {
		return result, nil
	}
	if err := appendPostTranslationInterchangeAudit(
		ctx,
		store,
		i.audit,
		strings.TrimSpace(job.RequestedByMemberID),
		job.EntityID,
		job.TargetLocale,
		targetPreviouslyExists,
	); err != nil {
		return TranslationInterchangeMutationResult{}, err
	}
	return result, nil
}

func postInterchangeTargetStorage(
	candidate *translation.Candidate,
	documentRevision string,
) (*contentv1.ContentStorageMutationBatch, error) {
	if candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return nil, domainerr.InvalidField("file_id", "Post target locale document is required")
	}
	mutations := candidate.RichTextLocaleMutations()
	if len(mutations) == 0 {
		return nil, nil
	}
	storage, err := contentv1.FlattenRichTextSystemMutationBatchStorage(
		&contentv1.RichTextBlockMutationBatch{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
			ExpectedRevision:        documentRevision,
			LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
				Locale: candidate.ContentBlockLocaleOverlay.GetLocale(), Mutations: mutations,
			}},
		},
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return nil, domainerr.InvalidField("file_id", err.Error())
	}
	return &storage, nil
}

func appendPostTranslationInterchangeAudit(
	ctx context.Context,
	store Store,
	auditWriter domainaudit.Appender,
	memberID string,
	postID string,
	locale string,
	targetPreviouslyExists bool,
) error {
	operation := sharedtelemetry.AuditItemOperationUpdated
	if !targetPreviouslyExists {
		operation = sharedtelemetry.AuditItemOperationCreated
	}
	return appendPostMemberLocaleContentAudit(ctx, store, auditWriter, memberID, postID, locale, operation)
}
