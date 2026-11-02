package emailcontent

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
)

// loadSubject reads the subject of the owner's locale row, reporting
// whether the row exists; a row may hold no subject.
func (o Owner[S]) loadSubject(ctx context.Context, store S, id, locale string, forUpdate bool) (*string, bool, error) {
	lock := Unlocked
	if forUpdate {
		lock = UpdateLock
	}
	row, exists, err := store.LocaleRow(ctx, id, locale, lock)
	if err != nil {
		return nil, false, errs.Internal(err)
	}
	return row.Subject, exists, nil
}

// ApplySubject stores the mutation's subject in its locale row with the
// content batch. The locale's presence must be what the caller observed,
// and the source subject cannot be empty.
func (o Owner[S]) ApplySubject(ctx context.Context, store S, mutation AIDocumentMutation, now time.Time) (contentblock.MetadataEffect, error) {
	current, exists, err := o.loadSubject(ctx, store, mutation.Reference, mutation.Locale, true)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if exists != mutation.ExpectedPresence {
		return contentblock.MetadataEffect{}, errs.FailedPrecondition(o.Name + " locale presence changed; reload before saving")
	}
	source := mutation.Locale == mutation.ExpectedSource
	if !exists && source {
		return contentblock.MetadataEffect{}, errs.FailedPrecondition(o.Name + " source locale is missing")
	}
	next := cloneString(current)
	if mutation.SetSubject {
		next = cloneString(&mutation.Subject)
	}
	if source && (next == nil || strings.TrimSpace(*next) == "") {
		return contentblock.MetadataEffect{}, errs.InvalidArgument("subject", o.Name+" source subject cannot be empty")
	}
	if exists && stringsEqual(current, next) {
		return contentblock.MetadataEffect{}, nil
	}
	if err := store.UpsertLocaleSubject(ctx, mutation.Reference, mutation.Locale, next, now); err != nil {
		return contentblock.MetadataEffect{}, errs.Internal(err)
	}
	return contentblock.MetadataEffect{
		Changed: true, AffectsTranslationSource: source,
		SourceLocale: mutation.ExpectedSource, ChangedLocales: []string{mutation.Locale},
	}, nil
}
