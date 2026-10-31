package entityparticipant

import (
	"context"
	"maps"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	"github.com/echovisionlab/geul-api/internal/structured"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// NullableStringUpdate is a parsed document-metadata string mutation. Present
// is false when the field is left alone; a nil Value clears it.
type NullableStringUpdate struct {
	Present bool
	Value   *string
}

// ParseNullableStringUpdate trims a set value and rejects an empty set, a
// false clear and an empty mutation.
func ParseNullableStringUpdate(field string, input *intrav1.NullableStringMutation) (NullableStringUpdate, error) {
	if input == nil {
		return NullableStringUpdate{}, nil
	}
	switch operation := input.Operation.(type) {
	case *intrav1.NullableStringMutation_Set:
		value := strings.TrimSpace(operation.Set)
		if value == "" {
			return NullableStringUpdate{}, errs.InvalidArgument(field, "set value must not be empty; use clear instead")
		}
		return NullableStringUpdate{Present: true, Value: &value}, nil
	case *intrav1.NullableStringMutation_Clear:
		if !operation.Clear {
			return NullableStringUpdate{}, errs.InvalidArgument(field, "clear must be true")
		}
		return NullableStringUpdate{Present: true}, nil
	default:
		return NullableStringUpdate{}, errs.InvalidArgument(field, "set or clear is required")
	}
}

// Changes reports whether the update gives current a different value.
func (u NullableStringUpdate) Changes(current *string) bool {
	return u.Present && !sameOptionalString(current, u.Value)
}

// Assign stores the update under column when it changes current.
func (u NullableStringUpdate) Assign(fields structured.Fields, column string, current *string) bool {
	if !u.Changes(current) {
		return false
	}
	if u.Value == nil {
		fields[column] = nil
	} else {
		fields[column] = *u.Value
	}
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
			return errs.InvalidArgument(s.ParentColumn, "must be a valid "+s.Title+" ID")
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

// AssignColumns checks the planned parent against the row, adds every
// changed shared column to fields and has slugAvailable check a changed slug.
// It reports whether a shared column changed.
func (s Spec) AssignColumns(
	ctx context.Context,
	tx *gorm.DB,
	entityID string,
	columns MetadataColumns,
	parent ParentUpdate,
	writesParent bool,
	current MetadataRow,
	fields structured.Fields,
	slugAvailable func(context.Context, *gorm.DB, string) error,
) (bool, error) {
	if _, err := s.CheckParent(ctx, tx, entityID, parent, current.Parent, writesParent); err != nil {
		return false, err
	}
	slugChanged := columns.Slug.Assign(fields, "slug", current.Slug)
	changed := slugChanged
	changed = columns.CountryCode.Assign(fields, "country_code", current.CountryCode) || changed
	changed = columns.Website.Assign(fields, "website", current.Website) || changed
	changed = columns.Parent.Assign(fields, s.ParentColumn, current.Parent) || changed
	if columns.SetSocialLinks && !maps.Equal(current.SocialLinks, columns.SocialLinks) {
		fields["social_links"] = columns.SocialLinks
		changed = true
	}
	if slugChanged && columns.Slug.Value != nil {
		if err := slugAvailable(ctx, tx, *columns.Slug.Value); err != nil {
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

// PlanParentUpdate reads the entity's parent outside the transaction, which
// decides whether the save needs an authorization mutation.
func (s Spec) PlanParentUpdate(ctx context.Context, db *gorm.DB, entityID string, update NullableStringUpdate) (ParentUpdate, error) {
	previous, err := s.LoadParent(ctx, db, entityID)
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
func (s Spec) CheckParent(ctx context.Context, tx *gorm.DB, entityID string, plan ParentUpdate, current *string, writes bool) (bool, error) {
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
	return true, s.ValidateParentTransition(ctx, tx, entityID, parentID)
}

// CheckParentUpdate is CheckParent for a save that runs its own
// authorization mutation; write is nil without one.
func (s Spec) CheckParentUpdate(ctx context.Context, tx *gorm.DB, entityID string, plan ParentUpdate, current *string, write authzmutation.WriteRelationships) (bool, error) {
	return s.CheckParent(ctx, tx, entityID, plan, current, write != nil)
}

// WriteParentRelationships moves the entity's SpiceDB parent from current to
// the planned parent.
func (s Spec) WriteParentRelationships(entityID string, plan ParentUpdate, current *string, write authzmutation.WriteRelationships) error {
	apply, compensate, err := s.ReplaceParentRelationships(entityID, current, plan.Update.Value)
	if err != nil {
		return err
	}
	return write(apply, compensate)
}

func (s Spec) parentChangedConcurrently() error {
	return errs.FailedPrecondition(s.Title + " parent changed concurrently; reload before saving")
}

// ExecuteMetadataMutation runs mutate inside an authorization mutation when
// the parent changes, and inside a plain transaction with a nil write
// otherwise.
func ExecuteMetadataMutation(ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, plan ParentUpdate, mutate func(*gorm.DB, authzmutation.WriteRelationships) error) error {
	if plan.RequiresWrite() {
		_, err := authzmutation.Execute(ctx, db, spiceDB, mutate)
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return mutate(tx, nil) })
}

// MetadataSave is one document-metadata save of the entity: it advances the
// entity's content document under Fence and moves the SpiceDB parent when
// the planned parent changes.
type MetadataSave struct {
	EntityID         string
	Documents        *contentblock.Store
	DocumentID       uuid.UUID
	ExpectedRevision uuid.UUID
	Parent           ParentUpdate
	// Access authorizes the save's contributors.
	Access mutation.Access
	Fence  contentblock.DomainFence
	// Apply writes the entity's metadata row under the fence. writesParent
	// tells CheckParent whether the save moves the SpiceDB parent.
	Apply func(ctx context.Context, tx *gorm.DB, writesParent bool) (MetadataApplied, error)
	// NormalizeError gives a content document failure the domain's answer.
	NormalizeError func(error) error
}

// MetadataApplied is what Apply changed and the parent it read under the
// lock, which the SpiceDB parent move starts from.
type MetadataApplied struct {
	Changed       bool
	CurrentParent *string
}

// SaveMetadata runs save through the mutation pipeline.
func (m Mutations) SaveMetadata(ctx context.Context, save MetadataSave) (contentblock.AdvanceResult, error) {
	writesParent := save.Parent.RequiresWrite()
	var currentParent *string
	spec := mutation.Spec[contentblock.AdvanceResult]{
		Access: save.Access,
		Change: func(ctx context.Context, tx *gorm.DB) (contentblock.AdvanceResult, error) {
			advanced, err := save.Documents.AdvanceRevision(
				ctx, tx, contentblock.AdvanceInput{DocumentID: save.DocumentID, ExpectedRevision: save.ExpectedRevision}, save.Fence,
				func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
					applied, err := save.Apply(ctx, tx, writesParent)
					currentParent = applied.CurrentParent
					return contentblock.MetadataEffect{Changed: applied.Changed}, err
				},
			)
			if err != nil {
				return contentblock.AdvanceResult{}, save.NormalizeError(err)
			}
			return advanced, nil
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
