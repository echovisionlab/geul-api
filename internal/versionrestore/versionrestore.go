// Package versionrestore holds the version restore flow Posts, Pages and
// Works share: put the version's source locale back, replace the content
// document with the version's snapshot, and restore the source title and
// summary, advancing the shared revision once for whatever changed. The
// domain locks and authorizes its root first, then publishes the effects of
// the outcome.
package versionrestore

import (
	"context"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/optional"
)

// Source is the source locale with the metadata a version restores.
type Source struct {
	Locale  string
	Title   *string
	Summary *string
}

// Store reads and mutates content inside the locked, authorized domain transaction.
type Store[S any] interface {
	LoadSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	ReplaceSnapshot(ctx context.Context, input contentblock.ReplaceInput) (contentblock.Result, error)
	AdvanceRevision(ctx context.Context, input contentblock.AdvanceInput, effect func(context.Context, S) (contentblock.MetadataEffect, error)) (contentblock.AdvanceResult, error)
}

// Restore binds the domain's metadata callbacks to its content Store.
type Restore[S any] struct {
	Store      Store[S]
	DocumentID uuid.UUID
	// Version is the source locale, title and summary the version stored.
	Version Source
	// LoadSource reads the current source locale and its metadata.
	LoadSource func(ctx context.Context, store S) (Source, error)
	// SwitchSourceLocale makes Version.Locale the source locale again; from is
	// the current source locale and revision the current shared revision.
	SwitchSourceLocale func(ctx context.Context, store S, from string, revision uuid.UUID) error
	// Replacement builds the snapshot replacement expected at revision.
	Replacement func(ctx context.Context, store S, revision uuid.UUID) (contentblock.ReplaceInput, error)
	// UpdateMetadata writes Version.Title and Version.Summary to the source
	// locale.
	UpdateMetadata func(ctx context.Context, store S) error
}

// Outcome says what a restore changed.
type Outcome struct {
	SourceLocaleSwitched bool
	TitleChanged         bool
	SummaryChanged       bool
	ContentChanged       bool
	// DocumentRevision is the shared revision after the restore.
	DocumentRevision uuid.UUID
}

// Changed reports whether the restore changed anything.
func (o Outcome) Changed() bool {
	return o.SourceLocaleSwitched || o.TitleChanged || o.SummaryChanged || o.ContentChanged
}

// Fields names the changed fields for a content.updated event in the order
// every domain publishes them.
func (o Outcome) Fields() []string {
	fields := make([]string, 0, 4)
	if o.TitleChanged {
		fields = append(fields, "title")
	}
	if o.SummaryChanged {
		fields = append(fields, "summary")
	}
	if o.ContentChanged {
		fields = append(fields, "content")
	}
	if o.SourceLocaleSwitched {
		fields = append(fields, "sourceLocale")
	}
	return fields
}

// RefreshesOG reports whether the restore changed what an OG image renders:
// the source title, or which locale is the source.
func (o Outcome) RefreshesOG() bool {
	return o.TitleChanged || o.SourceLocaleSwitched
}

// Apply runs the restore. A metadata-only restore still advances the shared
// revision, so collaborators reload the restored title and summary.
func (r Restore[S]) Apply(ctx context.Context, store S) (Outcome, error) {
	source, current, err := r.loadCurrent(ctx, store)
	if err != nil {
		return Outcome{}, err
	}
	var outcome Outcome
	if source.Locale != r.Version.Locale {
		if err := r.SwitchSourceLocale(ctx, store, source.Locale, current.Document.Revision); err != nil {
			return Outcome{}, err
		}
		outcome.SourceLocaleSwitched = true
		if source, current, err = r.loadCurrent(ctx, store); err != nil {
			return Outcome{}, err
		}
	}
	outcome.TitleChanged = !optional.Equal(source.Title, r.Version.Title)
	outcome.SummaryChanged = !optional.Equal(source.Summary, r.Version.Summary)

	input, err := r.Replacement(ctx, store, current.Document.Revision)
	if err != nil {
		return Outcome{}, err
	}
	replaced, err := r.Store.ReplaceSnapshot(ctx, input)
	if err != nil {
		return Outcome{}, err
	}
	outcome.ContentChanged, outcome.DocumentRevision = replaced.Changed, replaced.DocumentRevision
	if !outcome.TitleChanged && !outcome.SummaryChanged {
		return outcome, nil
	}
	if replaced.Changed {
		return outcome, r.UpdateMetadata(ctx, store)
	}
	advanced, err := r.Store.AdvanceRevision(
		ctx,
		contentblock.AdvanceInput{DocumentID: r.DocumentID, ExpectedRevision: replaced.DocumentRevision},
		func(ctx context.Context, store S) (contentblock.MetadataEffect, error) {
			if err := r.UpdateMetadata(ctx, store); err != nil {
				return contentblock.MetadataEffect{}, err
			}
			return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true}, nil
		},
	)
	if err != nil {
		return Outcome{}, err
	}
	outcome.DocumentRevision = advanced.DocumentRevision
	return outcome, nil
}

func (r Restore[S]) loadCurrent(ctx context.Context, store S) (Source, contentblock.Snapshot, error) {
	source, err := r.LoadSource(ctx, store)
	if err != nil {
		return Source{}, contentblock.Snapshot{}, err
	}
	snapshot, err := r.Store.LoadSnapshot(ctx, r.DocumentID, source.Locale)
	if err != nil {
		return Source{}, contentblock.Snapshot{}, err
	}
	return source, snapshot, nil
}

// Contributors credits the Member restoring the version, when there is one.
func Contributors(ctx context.Context) []string {
	if user := auth.GetUser(ctx); user != nil {
		return []string{user.MemberID.String()}
	}
	return nil
}
