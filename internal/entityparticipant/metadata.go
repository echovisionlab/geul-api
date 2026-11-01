package entityparticipant

import (
	"context"
	"maps"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// StringMutation is one document-metadata string mutation as a collaboration
// room sends it: Set a value, Clear it, or, when both are nil, name neither.
type StringMutation struct {
	Set   *string
	Clear *bool
}

// NullableStringUpdate is a parsed document-metadata string mutation. Present
// is false when the field is left alone; a nil Value clears it.
type NullableStringUpdate struct {
	Present bool
	Value   *string
}

// ParseNullableStringUpdate trims a set value and rejects an empty set, a
// false clear and an empty mutation. A nil input leaves the field alone.
func ParseNullableStringUpdate(field string, input *StringMutation) (NullableStringUpdate, error) {
	if input == nil {
		return NullableStringUpdate{}, nil
	}
	switch {
	case input.Set != nil:
		value := strings.TrimSpace(*input.Set)
		if value == "" {
			return NullableStringUpdate{}, domainerr.InvalidField(field, "set value must not be empty; use clear instead")
		}
		return NullableStringUpdate{Present: true, Value: &value}, nil
	case input.Clear != nil:
		if !*input.Clear {
			return NullableStringUpdate{}, domainerr.InvalidField(field, "clear must be true")
		}
		return NullableStringUpdate{Present: true}, nil
	default:
		return NullableStringUpdate{}, domainerr.InvalidField(field, "set or clear is required")
	}
}

// Changes reports whether the update gives current a different value.
func (u NullableStringUpdate) Changes(current *string) bool {
	return u.Present && !sameOptionalString(current, u.Value)
}

// Assignment is the new value of one nullable column a save changes; Value
// nil clears it.
type Assignment struct {
	Changed bool
	Value   *string
}

// Assign records the update in target when it changes current.
func (u NullableStringUpdate) Assign(target *Assignment, current *string) bool {
	if !u.Changes(current) {
		return false
	}
	*target = Assignment{Changed: true, Value: u.Value}
	return true
}

// MetadataColumns is the parsed document-metadata update of the columns
// Artist and Label share. Each domain parses the individual fields in its
// own order, so the first invalid field it reports stays the same.
type MetadataColumns struct {
	Slug           NullableStringUpdate
	CountryCode    NullableStringUpdate
	Website        NullableStringUpdate
	Parent         NullableStringUpdate
	SocialLinks    map[string]string
	SetSocialLinks bool
}

// Complete validates a set slug, canonicalizes a set parent ID and takes the
// social links when the update carries them (present).
func (s Spec) Complete(columns *MetadataColumns, socialLinksPresent bool, socialLinks map[string]string) error {
	if columns.Slug.Value != nil {
		if err := pathslug.Validate(*columns.Slug.Value); err != nil {
			return err
		}
	}
	if columns.Parent.Value != nil {
		parsed, err := uuid.Parse(*columns.Parent.Value)
		if err != nil {
			return domainerr.InvalidField(s.ParentColumn, "must be a valid "+s.Title+" ID")
		}
		canonical := parsed.String()
		columns.Parent.Value = &canonical
	}
	if socialLinksPresent {
		columns.SocialLinks = maps.Clone(socialLinks)
		if columns.SocialLinks == nil {
			columns.SocialLinks = map[string]string{}
		}
		columns.SetSocialLinks = true
	}
	return nil
}

// Present reports whether the update names any shared column.
func (c MetadataColumns) Present() bool {
	return c.Slug.Present || c.CountryCode.Present || c.Website.Present || c.Parent.Present || c.SetSocialLinks
}

// MetadataRow is the current value of the shared metadata columns, read
// under the save's lock.
type MetadataRow struct {
	Slug        *string
	CountryCode *string
	Website     *string
	Parent      *string
	SocialLinks map[string]string
}

// MetadataChange is what a document-metadata save changes of the shared
// columns.
type MetadataChange struct {
	Slug        Assignment
	CountryCode Assignment
	Website     Assignment
	Parent      Assignment
	// SocialLinks replaces the social links when SetSocialLinks is set.
	SocialLinks    map[string]string
	SetSocialLinks bool
}

// AssignColumns checks the planned parent against the row, records every
// changed shared column in change and has slugAvailable check a changed
// slug. It reports whether a shared column changed.
func (s Spec) AssignColumns(
	ctx context.Context,
	store Store,
	entityID string,
	columns MetadataColumns,
	parent ParentUpdate,
	writesParent bool,
	current MetadataRow,
	change *MetadataChange,
	slugAvailable func(ctx context.Context, slug string) error,
) (bool, error) {
	if _, err := s.CheckParent(ctx, store, entityID, parent, current.Parent, writesParent); err != nil {
		return false, err
	}
	slugChanged := columns.Slug.Assign(&change.Slug, current.Slug)
	changed := slugChanged
	changed = columns.CountryCode.Assign(&change.CountryCode, current.CountryCode) || changed
	changed = columns.Website.Assign(&change.Website, current.Website) || changed
	changed = columns.Parent.Assign(&change.Parent, current.Parent) || changed
	if columns.SetSocialLinks && !maps.Equal(current.SocialLinks, columns.SocialLinks) {
		change.SocialLinks, change.SetSocialLinks = columns.SocialLinks, true
		changed = true
	}
	if slugChanged && columns.Slug.Value != nil {
		if err := slugAvailable(ctx, *columns.Slug.Value); err != nil {
			return false, err
		}
	}
	return changed, nil
}

// ParentUpdate is a document-metadata parent change checked against the
// parent read before its transaction began.
type ParentUpdate struct {
	Update   NullableStringUpdate
	previous *string
}

// PlanParentUpdate reads the entity's parent before the save's transaction,
// which decides whether the save needs an authorization mutation.
func (s Spec) PlanParentUpdate(ctx context.Context, store Store, entityID string, update NullableStringUpdate) (ParentUpdate, error) {
	previous, err := s.LoadParent(ctx, store, entityID)
	if err != nil {
		return ParentUpdate{}, err
	}
	return ParentUpdate{Update: update, previous: previous}, nil
}

// RequiresWrite reports whether the planned change writes SpiceDB parent
// relationships.
func (p ParentUpdate) RequiresWrite() bool {
	return p.Update.Changes(p.previous)
}

// CheckParent validates the planned parent change against current, the
// parent read under the transaction, and reports whether it changes. writes
// is false when the save runs without an authorization mutation.
func (s Spec) CheckParent(ctx context.Context, store Store, entityID string, plan ParentUpdate, current *string, writes bool) (bool, error) {
	changed := plan.Update.Changes(current)
	if plan.Update.Present && (!sameOptionalString(current, plan.previous) || changed != plan.RequiresWrite()) {
		return false, s.parentChangedConcurrently()
	}
	if !changed {
		return false, nil
	}
	if !writes {
		return false, s.parentChangedConcurrently()
	}
	parentID := ""
	if plan.Update.Value != nil {
		parentID = *plan.Update.Value
	}
	return true, s.ValidateParentTransition(ctx, store, entityID, parentID)
}

func (s Spec) parentChangedConcurrently() error {
	return domainerr.Precondition(s.Title + " parent changed concurrently; reload before saving")
}

// MetadataSave is one document-metadata save of the entity: Save advances
// the entity's content document and writes its metadata row, and the save
// moves the SpiceDB parent when the planned parent changes.
type MetadataSave[S Store] struct {
	EntityID string
	Parent   ParentUpdate
	// Access authorizes the save's contributors.
	Access mutation.Access
	// Save advances the content document under the entity's fence and
	// writes the metadata row. writesParent tells CheckParent whether the
	// save moves the SpiceDB parent; currentParent is the parent Save read
	// under the lock, which the move starts from.
	Save func(ctx context.Context, store S, writesParent bool) (advanced contentblock.AdvanceResult, currentParent *string, err error)
}

// SaveMetadata runs save through the mutation pipeline.
func (m Mutations[S]) SaveMetadata(ctx context.Context, save MetadataSave[S]) (contentblock.AdvanceResult, error) {
	writesParent := save.Parent.RequiresWrite()
	var currentParent *string
	spec := mutation.StoreSpec[contentblock.AdvanceResult, S]{
		Access: save.Access,
		Change: func(ctx context.Context, store S) (contentblock.AdvanceResult, error) {
			advanced, parent, err := save.Save(ctx, store, writesParent)
			currentParent = parent
			return advanced, err
		},
		// Telemetry has no document-metadata record; these saves have never
		// been audited.
		Audit: func(contentblock.AdvanceResult) mutation.Audit { return mutation.Unchanged() },
	}
	if writesParent {
		spec.Relationships = func(contentblock.AdvanceResult) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return m.spec.ReplaceParentRelationships(save.EntityID, currentParent, save.Parent.Update.Value)
		}
	}
	return mutation.Run(ctx, m.runner, spec)
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
