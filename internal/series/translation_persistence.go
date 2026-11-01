package series

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// ProviderTranslationJob is the translation job a provider candidate
// answers.
type ProviderTranslationJob struct {
	EntityType          string
	EntityID            string
	TargetLocale        string
	RequestedByMemberID string
}

// ApplyProviderTranslationCandidate patches only surviving request-time
// Series locale fields in the translation application's transaction store
// is bound to. If the requested target became the current source, the same
// accepted response advances the shared document revision instead of
// manufacturing a target revision.
func ApplyProviderTranslationCandidate(
	ctx context.Context,
	store TranslationStore,
	job ProviderTranslationJob,
	candidate *translation.Candidate,
	input translation.EntryWrite,
	audit domainaudit.Appender,
) error {
	if store == nil || job.EntityType != "series" || candidate == nil || !candidate.HasProviderUnitPatch() {
		return errors.New("post Series provider translation candidate is required")
	}
	if audit == nil {
		return errors.New("post Series provider translation Audit writer is required")
	}
	memberID, err := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
	if err != nil || memberID == uuid.Nil || memberID.String() != strings.TrimSpace(job.RequestedByMemberID) {
		return fmt.Errorf("post Series provider translation requires canonical requester Member, got %q", job.RequestedByMemberID)
	}
	now := input.Now.UTC()
	if now.IsZero() {
		return errors.New("Post Series provider translation time is required")
	}
	current, err := loadSeriesInterchangeState(ctx, store, job.EntityID, job.TargetLocale, true)
	if err != nil {
		return err
	}
	next := providerPatchedCopy(job.EntityID, current.target, candidate)
	if current.exists && current.target.sameCopy(next) {
		return nil
	}
	operation, err := writeProviderTranslation(ctx, store, job, current, next, now)
	if err != nil {
		return err
	}
	if job.TargetLocale == current.sourceLocale {
		state, err := loadSeriesContentDocumentState(ctx, store, job.EntityID, false)
		if err != nil {
			return err
		}
		if _, err := advanceSeriesContentDocument(ctx, store, job.EntityID, state.ID, state.Revision, now); err != nil {
			return err
		}
	}
	return store.AppendMemberAudit(
		ctx, audit, memberID.String(), sharedtelemetry.AuditPostSeriesUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostSeriesLocaleContentAuditRecord(
				metadata, job.EntityID, job.TargetLocale, operation,
			)
		},
	)
}

// providerPatchedCopy is the locale copy with the provider's surviving
// entity units applied.
func providerPatchedCopy(seriesID string, current Translation, candidate *translation.Candidate) Translation {
	patch, _ := candidate.ProviderPatch()
	next := current
	for _, unit := range patch.Units {
		result, ok := patch.Results[unit.UnitID]
		if !ok || unit.ContainerType != translation.ContainerTypeEntity || unit.ContainerID != seriesID {
			continue
		}
		value := result.TranslatedText
		switch unit.UnitID {
		case "entity:title":
			next.Title = &value
		case "entity:summary":
			next.Summary = &value
		case "entity:content_text":
			next.ContentText = &value
		}
	}
	return next
}

// writeProviderTranslation updates or creates the target locale row and
// returns the audited item operation.
func writeProviderTranslation(
	ctx context.Context,
	store TranslationStore,
	job ProviderTranslationJob,
	current seriesInterchangeState,
	next Translation,
	now time.Time,
) (sharedtelemetry.AuditItemOperation, error) {
	if current.exists {
		updatedAt := translation.NextTargetUpdatedAt(now, current.target.UpdatedAt)
		err := store.UpdateTranslation(ctx, job.EntityID, job.TargetLocale, next.Title, next.Summary, next.ContentText, updatedAt)
		if errors.Is(err, ErrTranslationNotFound) {
			return "", domainerr.Precondition("Post Series locale changed; retry")
		}
		return sharedtelemetry.AuditItemOperationUpdated, err
	}
	if job.TargetLocale == current.sourceLocale {
		return "", domainerr.Precondition("Post Series source locale copy is missing")
	}
	return sharedtelemetry.AuditItemOperationCreated, store.InsertTranslation(ctx, Translation{
		SeriesID: job.EntityID, Locale: job.TargetLocale, Title: next.Title, Summary: next.Summary,
		ContentText: next.ContentText, CreatedAt: now, UpdatedAt: now,
	})
}

// sameCopy reports whether next carries the same title, summary and text.
func (t Translation) sameCopy(next Translation) bool {
	return equalOptionalString(t.Title, next.Title) &&
		equalOptionalString(t.Summary, next.Summary) &&
		equalOptionalString(t.ContentText, next.ContentText)
}

// saveSourceCopy persists the authoritative Series copy, makes it the only
// source-locale row, and keeps the Series root projection aligned in the
// same transaction.
func saveSourceCopy(
	ctx context.Context,
	store TranslationStore,
	seriesID string,
	locale string,
	title *string,
	summary *string,
	now time.Time,
) error {
	if title == nil || strings.TrimSpace(*title) == "" {
		return domainerr.Required("title")
	}
	if err := store.UpsertTranslation(ctx, seriesID, locale, TranslationWrite{
		Title:       title,
		Summary:     summary,
		ContentText: summary,
		Now:         now,
	}); err != nil {
		return err
	}
	return store.SetSourceLocale(ctx, seriesID, locale, now)
}
