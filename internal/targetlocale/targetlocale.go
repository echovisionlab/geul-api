// Package targetlocale holds the rules every domain applies when it writes
// one target (non-source) locale of a content document: what a write may do,
// which target revision it must match, and which document it may store to.
// The domain keeps its own metadata rows and the checks whose answers differ
// by domain.
package targetlocale

import (
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// RevisionMode says which target revision a write must match.
type RevisionMode int

const (
	// RevisionObserved requires the target revision the caller read.
	// Interactive Collab, DCDP and interchange writers use it.
	RevisionObserved RevisionMode = iota
	// RevisionCurrent accepts the target revision current under the domain
	// row lock. Translation provider delivery uses it to overwrite exactly
	// the locale values it delivers.
	RevisionCurrent
)

// Policy says what one target-locale write may do.
type Policy struct {
	// AllowCreate lets the write create an absent target locale.
	AllowCreate bool
	// SeedSource fills a locale the write creates with the current source
	// values before the requested values apply.
	SeedSource bool
	// AllowLocaleDeletes lets an authoritative whole-locale replacement
	// delete locale values. Interactive writers keep every locale unit and
	// write an explicit empty value instead.
	AllowLocaleDeletes bool
	RevisionMode       RevisionMode
}

// State is the target-locale state a domain loaded under its row lock.
type State struct {
	// Entity names the owner in client messages, such as "Work".
	Entity         string
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	TargetExists   bool
	TargetRevision string
}

// Admit checks a write of batch to locale against the policy and the locked
// state, and returns the batch to store: a copy, seeded from the source when
// the write creates the locale and the policy asks for it.
func (p Policy) Admit(
	state State,
	locale string,
	batch contentblock.Batch,
	expectedTargetRevision *string,
) (contentblock.Batch, error) {
	document := state.Snapshot.Document
	if err := contentblock.ValidateTargetLocaleBatch(
		state.Entity, batch, document.ID, document.Revision, locale, p.AllowLocaleDeletes,
	); err != nil {
		return contentblock.Batch{}, err
	}
	if err := state.CheckRevision(expectedTargetRevision, p.RevisionMode); err != nil {
		return contentblock.Batch{}, err
	}
	if !state.TargetExists && !p.AllowCreate {
		return contentblock.Batch{}, errs.FailedPrecondition(
			state.Entity + " target locale must be explicitly created before collaboration",
		)
	}
	admitted := contentblock.CloneBatch(batch)
	if !state.TargetExists && p.SeedSource {
		return contentblock.SeedTargetLocaleBatch(admitted, state.Snapshot, state.SourceLocale, locale)
	}
	return admitted, nil
}

// CheckRevision is the target compare-and-set: an absent target takes no
// revision, a present one takes its current revision unless mode accepts the
// current one. A conflict carries the shared document revision observed
// under the lock.
func (state State) CheckRevision(expected *string, mode RevisionMode) error {
	if err := translation.ValidateTargetRevisionWrite(
		expected, state.TargetRevision, state.TargetExists, mode == RevisionCurrent,
	); err != nil {
		return translation.WithDocumentRevision(err, state.Snapshot.Document.Revision.String())
	}
	return nil
}

// DeleteGroups returns the locale groups that remove every stored value of
// locale, in a stable Block order, or nil when the locale stores none.
func DeleteGroups(snapshot contentblock.Snapshot, locale string) []contentblock.LocaleMutationGroup {
	for _, overlay := range snapshot.LocaleOverlays {
		if overlay.Locale != locale || len(overlay.Blocks) == 0 {
			continue
		}
		group := contentblock.LocaleMutationGroup{Locale: locale, Deletes: make([]uuid.UUID, 0, len(overlay.Blocks))}
		for _, block := range overlay.Blocks {
			group.Deletes = append(group.Deletes, block.BlockID)
		}
		sort.Slice(group.Deletes, func(i, j int) bool { return group.Deletes[i].String() < group.Deletes[j].String() })
		return []contentblock.LocaleMutationGroup{group}
	}
	return nil
}

// SnapshotHasLocale reports whether the snapshot holds an overlay of locale.
// Overlay presence is the durable locale boundary: an overlay of explicit
// empty values is still a stored locale.
func SnapshotHasLocale(snapshot contentblock.Snapshot, locale string) bool {
	for _, overlay := range snapshot.LocaleOverlays {
		if overlay.Locale == locale {
			return true
		}
	}
	return false
}

// WriteTime is the stored time of a write: now at database precision, or the
// current time when the caller passed none.
func WriteTime(now time.Time) time.Time {
	if now.IsZero() {
		return dbtime.Now()
	}
	return now.UTC().Truncate(dbtime.Precision)
}
