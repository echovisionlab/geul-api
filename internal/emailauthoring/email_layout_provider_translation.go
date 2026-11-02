package emailauthoring

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/translation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// ApplyEmailLayoutProviderTranslationCandidate applies the request-time unit
// results that still exist in the current marker graph. A target locale that
// became source-owned advances the shared Content Document revision.
func ApplyEmailLayoutProviderTranslationCandidate(
	ctx context.Context,
	joined Joined,
	job emailcontent.TranslationJob,
	candidate *translation.Candidate,
	input translation.EntryWrite,
) error {
	store := joined.Store
	if job.EntityType != "email_layout" || candidate == nil || !candidate.HasProviderUnitPatch() {
		return errors.New("email layout provider translation dependencies are required")
	}
	memberID, err := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
	if err != nil || memberID == uuid.Nil || memberID.String() != strings.TrimSpace(job.RequestedByMemberID) {
		return fmt.Errorf("Email Layout provider translation requires canonical requester Member: %w", err)
	}
	now := input.Now.UTC()
	if now.IsZero() {
		return errors.New("Email Layout provider translation time is required")
	}
	apply, err := loadEmailLayoutProviderApply(ctx, store, job, candidate)
	if err != nil || apply.unchanged() {
		return err
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if job.TargetLocale == apply.authority.SourceLocale {
		err = apply.saveSource(ctx, store, now)
	} else {
		_, err = saveEmailLayoutTarget(ctx, store, job.EntityID, job.TargetLocale, apply.sourceHTML, apply.next, now, emailLayoutInterchangeUpdatedAt(apply.entry))
		if apply.entry == nil {
			operation = sharedtelemetry.AuditItemOperationCreated
		}
	}
	if err != nil {
		return err
	}
	return domainaudit.AppendMember(ctx, nil, joined.Audit, memberID.String(), sharedtelemetry.AuditEmailLayoutUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewEmailLayoutLocaleContentAuditRecord(metadata, job.EntityID, job.TargetLocale, operation)
	})
}

// layoutProviderApply is a provider translation's current and patched unit
// values under the locked layout authority.
type layoutProviderApply struct {
	authority  LayoutAuthority
	sourceHTML string
	entry      *emailutil.LayoutTranslationEntry
	current    map[string]string
	next       map[string]string
}

func (a layoutProviderApply) unchanged() bool {
	return maps.Equal(a.current, a.next) && a.entry != nil
}

// loadEmailLayoutProviderApply locks the layout and its target row and
// patches the target's current values with the provider's results for units
// the current source still marks.
func loadEmailLayoutProviderApply(
	ctx context.Context,
	store Store,
	job emailcontent.TranslationJob,
	candidate *translation.Candidate,
) (layoutProviderApply, error) {
	if err := lockEmailLayoutAIDocumentRoot(ctx, store, job.EntityID, emailcontent.UpdateLock); err != nil {
		return layoutProviderApply{}, err
	}
	if err := store.RequireLayoutMutable(ctx, job.EntityID); err != nil {
		return layoutProviderApply{}, err
	}
	authority, err := loadEmailLayoutDocumentAuthority(ctx, store, job.EntityID, emailcontent.UpdateLock)
	if err != nil {
		return layoutProviderApply{}, err
	}
	source, err := store.CanonicalLayoutSource(ctx, job.EntityID, authority.SourceLocale)
	if err != nil {
		return layoutProviderApply{}, err
	}
	apply := layoutProviderApply{authority: authority, sourceHTML: derefString(source.ContentHTML), current: make(map[string]string)}
	descriptors, err := emailutil.ExtractLayoutContentUnits(apply.sourceHTML)
	if err != nil {
		return layoutProviderApply{}, domainerr.Precondition("Email Layout source unit markers require repair before provider apply").WithCause(failurecause.ServerFault(err))
	}
	if apply.entry, err = store.LockLayoutTranslationEntry(ctx, job.EntityID, job.TargetLocale, emailcontent.UpdateLock); err != nil {
		return layoutProviderApply{}, err
	}
	if job.TargetLocale == authority.SourceLocale {
		for _, descriptor := range descriptors {
			apply.current[descriptor.Handle] = descriptor.SourceValue
		}
	} else if apply.entry != nil && apply.entry.ContentHTML != nil {
		if apply.current, err = emailutil.ExtractLayoutStoredLocaleValues(*apply.entry.ContentHTML); err != nil {
			return layoutProviderApply{}, domainerr.Precondition("Email Layout target unit markers require repair before provider apply").WithCause(failurecause.ServerFault(err))
		}
	}
	apply.next = maps.Clone(apply.current)
	allowed := make(map[string]struct{}, len(descriptors))
	for _, descriptor := range descriptors {
		allowed[descriptor.Handle] = struct{}{}
	}
	patch, _ := candidate.ProviderPatch()
	for _, unit := range patch.Units {
		result, ok := patch.Results[unit.UnitID]
		if !ok || unit.ContainerType != translation.ContainerTypeHTMLNode || unit.ContainerID != unit.UnitID {
			continue
		}
		if _, exists := allowed[unit.UnitID]; exists {
			apply.next[unit.UnitID] = result.TranslatedText
		}
	}
	return apply, nil
}

// saveSource stores the patched values as the source locale's next document
// revision, which must advance the translation source.
func (a layoutProviderApply) saveSource(ctx context.Context, store Store, now time.Time) error {
	contentHTML, contentText, err := emailutil.ApplyLayoutSourceValues(a.sourceHTML, a.next)
	if err != nil {
		return domainerr.InvalidField("content", err.Error())
	}
	result, err := saveEmailLayoutSource(
		ctx, store, a.authority.LayoutID, a.authority, a.authority.DocumentRevision,
		&emailutil.LayoutTranslationDocument{ContentHTML: contentHTML, ContentText: contentText}, now,
	)
	if err != nil {
		return err
	}
	if !result.Changed || !result.TranslationSourceChanged {
		return errors.New("provider source Email Layout translation did not advance source state")
	}
	return nil
}
