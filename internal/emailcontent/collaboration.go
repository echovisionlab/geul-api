package emailcontent

import (
	"context"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// CollaborationAccess is the access of a collaboration room write.
// editor-collab calls as itself: the owner's contributor fence inside the
// write authorizes the room's contributors.
var CollaborationAccess = mutation.Locked(func(context.Context, any, mutation.Require) error { return nil })

// CollaborationChange is what a collaboration room write changed and the
// record it names.
type CollaborationChange struct {
	DocumentRevision string
	Changed          bool
	SourceChanged    bool
	ChangedLocales   []string
	Locale           string
	// TargetRevision is the target locale's revision after a target write.
	TargetRevision *string
	Record         mutation.Audit
}

// LocaleContentChange is the record of a change to id's content in locale,
// credited to memberID.
func (o Owner[S]) LocaleContentChange(memberID, id, locale string, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
	return mutation.MemberAudit(memberID, o.LocaleContentAudit.Action, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return o.LocaleContentAudit.Record(metadata, id, locale, operation)
	})
}

// DecodeCollaborationBatch decodes a room's Block batch for documentID,
// restoring the locale values its affected leaves keep. Every locale group
// must be the room's locale.
func (o Owner[S]) DecodeCollaborationBatch(
	documentID uuid.UUID,
	locale string,
	batch *contentv1.RichTextBlockMutationBatch,
	affected []*managev1.AIDocumentFieldTarget,
) (contentblock.Batch, error) {
	storage, err := contentv1.FlattenRichTextMutationBatchStorage(batch, contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE)
	if err != nil {
		return contentblock.Batch{}, domainerr.InvalidField("batch", err.Error())
	}
	if err := contentblock.RestoreRichTextAffectedLocaleValues(batch.GetProfile(), locale, &storage, affected); err != nil {
		return contentblock.Batch{}, domainerr.InvalidField("affected_locale_values", err.Error())
	}
	decoded, err := contentblock.BatchFromRichTextStorage(documentID, batch.GetProfile(), storage)
	if err != nil {
		return contentblock.Batch{}, o.NormalizeContentBlockError(err)
	}
	for _, group := range decoded.LocaleGroups {
		if group.Locale != locale {
			return contentblock.Batch{}, domainerr.InvalidField("locale", o.Name+" Block mutation must match the authenticated room locale")
		}
	}
	return decoded, nil
}

// SaveCollaborationTarget writes a target room's locale and credits a
// change to originMemberID.
func (o Owner[S]) SaveCollaborationTarget(
	ctx context.Context,
	store S,
	input TargetLocaleMutation,
	originMemberID string,
) (CollaborationChange, error) {
	output, err := o.ApplyTargetLocaleMutation(ctx, store, input)
	if err != nil {
		return CollaborationChange{}, err
	}
	change := CollaborationChange{
		DocumentRevision: output.Result.DocumentRevision.String(), Changed: output.Result.Changed,
		ChangedLocales: output.Result.ChangedLocales, Locale: input.Locale, TargetRevision: &output.TargetRevision,
		Record: mutation.Unchanged(),
	}
	if change.Changed {
		change.Record = o.LocaleContentChange(originMemberID, input.ID, input.Locale, LocaleContentOperation(false, false, false, true))
	}
	return change, nil
}

// CollaborationDocument is an owner's document in one locale as a room
// loads it.
type CollaborationDocument struct {
	State               ExactLocaleState
	Document            *contentv1.LocalizedRichTextDocument
	Locale              string
	PresentLocaleValues []*managev1.AIDocumentFieldTarget
	// TargetRevision is set for an existing target locale.
	TargetRevision *string
}

// LoadCollaborationDocument reads id's document in locale in one snapshot
// of units under a lock of its root row, which lock takes and which returns
// the document's ID.
func (o Owner[S]) LoadCollaborationDocument(
	ctx context.Context,
	units mutation.UnitOfWork[S],
	id string,
	locale string,
	lock func(ctx context.Context, store S) (uuid.UUID, error),
) (CollaborationDocument, error) {
	var state ExactLocaleState
	err := units.ReadLocked(ctx, func(store S) error {
		documentID, err := lock(ctx, store)
		if err != nil {
			return err
		}
		state, err = o.LoadExactLocaleState(ctx, store, id, documentID, locale, false)
		return err
	})
	if err != nil {
		return CollaborationDocument{}, err
	}
	document, err := o.materializeLocale(state, locale)
	if err != nil {
		return CollaborationDocument{}, err
	}
	present, err := contentblock.PresentRichTextLocaleValues(state.Snapshot, locale)
	if err != nil {
		return CollaborationDocument{}, o.NormalizeContentBlockError(err)
	}
	loaded := CollaborationDocument{State: state, Document: document, Locale: locale, PresentLocaleValues: present}
	if locale != state.SourceLocale && state.TargetMetadata != nil {
		targetRevision := state.TargetRevision
		loaded.TargetRevision = &targetRevision
	}
	return loaded, nil
}
