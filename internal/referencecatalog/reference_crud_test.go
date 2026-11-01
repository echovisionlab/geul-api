package referencecatalog_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// referenceKind is one reference kind's use cases and contract.
type referenceKind struct {
	kind      referencecatalog.ReferenceKind
	maxLength int
	described bool
	menu      bool
	policy    interface {
		List() (policyv1.Can, error)
		Create() (policyv1.Can, error)
		Edit(string) (policyv1.Can, error)
		Delete(string) (policyv1.Can, error)
		TouchPolicy(string) (policyv1.RelationshipMutation, error)
		DeletePolicy(string) (policyv1.RelationshipMutation, error)
	}
	created, updated, deleted sharedtelemetry.AuditAction
	list                      func(*referencecatalog.Service, context.Context, referencecatalog.ReferenceListQuery) (referencecatalog.Page[referencecatalog.Reference], error)
	listAdmin                 func(*referencecatalog.Service, context.Context, referencecatalog.ReferenceListQuery) (referencecatalog.Page[referencecatalog.ReferenceUsage], error)
	create                    func(*referencecatalog.Service, context.Context, referencecatalog.CreateReferenceCommand) (referencecatalog.Reference, error)
	update                    func(*referencecatalog.Service, context.Context, referencecatalog.UpdateReferenceCommand) (referencecatalog.Reference, error)
	delete                    func(*referencecatalog.Service, context.Context, referencecatalog.DeleteReferenceCommand) error
}

var referenceKinds = []referenceKind{
	{
		kind: referencecatalog.KindCategory, maxLength: 100, described: true, menu: true, policy: policyv1.Category,
		created: sharedtelemetry.AuditCategoryCreated, updated: sharedtelemetry.AuditCategoryUpdated, deleted: sharedtelemetry.AuditCategoryDeleted,
		list: (*referencecatalog.Service).ListCategories, listAdmin: (*referencecatalog.Service).ListCategoriesAdmin,
		create: (*referencecatalog.Service).CreateCategory, update: (*referencecatalog.Service).UpdateCategory, delete: (*referencecatalog.Service).DeleteCategory,
	},
	{
		kind: referencecatalog.KindTag, maxLength: 50, menu: true, policy: policyv1.Tag,
		created: sharedtelemetry.AuditTagCreated, updated: sharedtelemetry.AuditTagUpdated, deleted: sharedtelemetry.AuditTagDeleted,
		list: (*referencecatalog.Service).ListTags, listAdmin: (*referencecatalog.Service).ListTagsAdmin,
		create: (*referencecatalog.Service).CreateTag, update: (*referencecatalog.Service).UpdateTag, delete: (*referencecatalog.Service).DeleteTag,
	},
	{
		kind: referencecatalog.KindGenre, maxLength: 100, described: true, policy: policyv1.Genre,
		created: sharedtelemetry.AuditGenreCreated, updated: sharedtelemetry.AuditGenreUpdated, deleted: sharedtelemetry.AuditGenreDeleted,
		list: (*referencecatalog.Service).ListGenres, listAdmin: (*referencecatalog.Service).ListGenresAdmin,
		create: (*referencecatalog.Service).CreateGenre, update: (*referencecatalog.Service).UpdateGenre, delete: (*referencecatalog.Service).DeleteGenre,
	},
	{
		kind: referencecatalog.KindStyle, maxLength: 100, described: true, policy: policyv1.Style,
		created: sharedtelemetry.AuditStyleCreated, updated: sharedtelemetry.AuditStyleUpdated, deleted: sharedtelemetry.AuditStyleDeleted,
		list: (*referencecatalog.Service).ListStyles, listAdmin: (*referencecatalog.Service).ListStylesAdmin,
		create: (*referencecatalog.Service).CreateStyle, update: (*referencecatalog.Service).UpdateStyle, delete: (*referencecatalog.Service).DeleteStyle,
	},
	{
		kind: referencecatalog.KindFormat, maxLength: 50, policy: policyv1.Format,
		created: sharedtelemetry.AuditFormatCreated, updated: sharedtelemetry.AuditFormatUpdated, deleted: sharedtelemetry.AuditFormatDeleted,
		list: (*referencecatalog.Service).ListFormats, listAdmin: (*referencecatalog.Service).ListFormatsAdmin,
		create: (*referencecatalog.Service).CreateFormat, update: (*referencecatalog.Service).UpdateFormat, delete: (*referencecatalog.Service).DeleteFormat,
	},
}

func (k referenceKind) resource() string { return string(k.kind) }

func (k referenceKind) can(t *testing.T, build func() (policyv1.Can, error)) policyv1.Can {
	t.Helper()
	can, err := build()
	require.NoError(t, err)
	return can
}

func forEachKind(t *testing.T, run func(t *testing.T, k referenceKind)) {
	for _, k := range referenceKinds {
		t.Run(k.resource(), func(t *testing.T) {
			t.Parallel()
			run(t, k)
		})
	}
}

func TestReferenceCreateNormalizesAuditsAndWritesThePolicy(t *testing.T) {
	forEachKind(t, func(t *testing.T, k referenceKind) {
		f, p := newFixture(t, nil, nil), newPrincipal(t)
		description := "Ambient and drone"

		created, err := k.create(f.service, p.ctx, referencecatalog.CreateReferenceCommand{Name: "  Deep Ambient/Drone  ", Description: &description})
		require.NoError(t, err)

		require.Equal(t, "Deep Ambient/Drone", created.Name)
		require.Equal(t, "deep-ambient-drone", created.Slug, "the slug derives from the trimmed name")
		stored, ok := f.store.StoredReference(k.kind, created.ID)
		require.True(t, ok)
		require.Equal(t, created, stored)
		if k.described {
			require.Equal(t, description, *created.Description)
		} else {
			require.Nil(t, created.Description, "a kind without a description ignores it")
		}
		f.requireAudited(t, p, audited(k.created, k.resource(), created.ID))
		require.Equal(t, [][]policyv1.RelationshipMutation{relationship(t, k.policy.TouchPolicy, created.ID)}, f.checker.Applied())
		require.Equal(t, []authztest.Check{authztest.Of(p.identityID, k.can(t, k.policy.Create), nil)}, f.checker.Checks())
		require.Equal(t, []mutation.Step{
			mutation.StepAuthorizationLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepRelationships, mutation.StepCommit,
		}, f.units.Steps())
	})
}

func TestReferenceCreateTrimsAnExplicitSlug(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	slug := "  ambient-drone  "
	created, err := f.service.CreateGenre(p.ctx, referencecatalog.CreateReferenceCommand{Name: "Ambient", Slug: &slug})
	require.NoError(t, err)
	require.Equal(t, "ambient-drone", created.Slug)
}

func TestReferenceWritesRejectInvalidNamesAndSlugsAtTheColumnLength(t *testing.T) {
	forEachKind(t, func(t *testing.T, k referenceKind) {
		f, p := newFixture(t, nil, nil), newPrincipal(t)
		existing := f.store.SeedReference(k.kind, referencecatalog.Reference{Name: "Existing", Slug: "existing"})
		blank, slashed := "   ", "a/b"
		tooLong := strings.Repeat("가", k.maxLength+1)
		longest := strings.Repeat("가", k.maxLength)

		for name, attempt := range map[string]func() error{
			"blank name": func() error {
				_, err := k.create(f.service, p.ctx, referencecatalog.CreateReferenceCommand{Name: blank})
				return err
			},
			"too long name": func() error {
				_, err := k.create(f.service, p.ctx, referencecatalog.CreateReferenceCommand{Name: tooLong})
				return err
			},
			"blank slug": func() error {
				_, err := k.create(f.service, p.ctx, referencecatalog.CreateReferenceCommand{Name: "Named", Slug: &blank})
				return err
			},
			"slug with a slash": func() error {
				_, err := k.create(f.service, p.ctx, referencecatalog.CreateReferenceCommand{Name: "Named", Slug: &slashed})
				return err
			},
			"blank name update": func() error {
				_, err := k.update(f.service, p.ctx, referencecatalog.UpdateReferenceCommand{ID: existing.ID, Name: &blank})
				return err
			},
			"too long slug update": func() error {
				_, err := k.update(f.service, p.ctx, referencecatalog.UpdateReferenceCommand{ID: existing.ID, Slug: &tooLong})
				return err
			},
			"too long name update": func() error {
				_, err := k.update(f.service, p.ctx, referencecatalog.UpdateReferenceCommand{ID: existing.ID, Name: &tooLong})
				return err
			},
		} {
			require.Equal(t, connect.CodeInvalidArgument, codeOf(attempt()), name)
		}
		require.Empty(t, f.audit.Records())
		require.Equal(t, mutation.StepRollback, f.lastStep())

		created, err := k.create(f.service, p.ctx, referencecatalog.CreateReferenceCommand{Name: longest, Slug: &longest})
		require.NoError(t, err, "the column length itself is accepted")
		require.Equal(t, longest, created.Name)
	})
}

func TestReferenceWritesAnswerTakenNamesAndSlugs(t *testing.T) {
	forEachKind(t, func(t *testing.T, k referenceKind) {
		f, p := newFixture(t, nil, nil), newPrincipal(t)
		f.store.SeedReference(k.kind, referencecatalog.Reference{Name: "Taken", Slug: "taken"})
		other := f.store.SeedReference(k.kind, referencecatalog.Reference{Name: "Other", Slug: "other"})
		taken, takenSlug := "Taken", "taken"

		_, err := k.create(f.service, p.ctx, referencecatalog.CreateReferenceCommand{Name: taken, Slug: new("fresh")})
		require.Equal(t, connect.CodeAlreadyExists, codeOf(err))
		require.Equal(t, k.resource()+" with name 'Taken' already exists", messageOf(err))
		require.ErrorIs(t, err, referencecatalog.ErrNameTaken, "the cause stays in the chain")

		_, err = k.update(f.service, p.ctx, referencecatalog.UpdateReferenceCommand{ID: other.ID, Slug: &takenSlug})
		require.Equal(t, connect.CodeAlreadyExists, codeOf(err))
		require.Equal(t, k.resource()+" with slug 'taken' already exists", messageOf(err))

		f.store.Fail("InsertReference", referencecatalog.ErrReferenceTaken)
		_, err = k.create(f.service, p.ctx, referencecatalog.CreateReferenceCommand{Name: "Fresh"})
		require.Equal(t, connect.CodeAlreadyExists, codeOf(err))
		require.Equal(t, k.resource()+" with the same name or slug already exists", messageOf(err))
		require.Empty(t, f.audit.Records())
	})
}

func TestReferenceUpdateStoresOnlyChangedFieldsAndMovesMenuTargets(t *testing.T) {
	forEachKind(t, func(t *testing.T, k referenceKind) {
		f, p := newFixture(t, nil, nil), newPrincipal(t)
		description := "old"
		existing := f.store.SeedReference(k.kind, referencecatalog.Reference{Name: "Old", Slug: "old-slug", Description: &description})
		name, slug, nextDescription := " New ", "new-slug", "new"
		command := referencecatalog.UpdateReferenceCommand{ID: existing.ID, Name: &name, Slug: &slug}
		changed := []string{"name", "slug"}
		if k.described {
			command.Description = &nextDescription
			changed = []string{"description", "name", "slug"}
		}

		updated, err := k.update(f.service, p.ctx, command)
		require.NoError(t, err)

		require.Equal(t, "New", updated.Name)
		require.Equal(t, "new-slug", updated.Slug)
		f.requireAudited(t, p, audited(k.updated, k.resource(), existing.ID, changed...))
		require.Equal(t, []authztest.Check{authztest.Of(p.identityID, k.can(t, func() (policyv1.Can, error) { return k.policy.Edit(existing.ID) }), nil)}, f.checker.Checks())
		require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepCommit}, f.units.Steps())
		require.Empty(t, f.checker.Applied(), "an update writes no relationship")
		if k.menu {
			require.Equal(t, []referencecatalog.MenuTargetSlugChange{{
				Target: referencecatalog.MenuTarget{LinkType: k.resource(), ID: existing.ID, Slug: "old-slug"}, NextSlug: "new-slug",
			}}, f.store.MenuTargetSlugChanges())
		} else {
			require.Empty(t, f.store.MenuTargetSlugChanges())
		}

		moved := len(f.store.MenuTargetSlugChanges())
		renamed := "Renamed"
		_, err = k.update(f.service, p.ctx, referencecatalog.UpdateReferenceCommand{ID: existing.ID, Name: &renamed})
		require.NoError(t, err)
		require.Len(t, f.store.MenuTargetSlugChanges(), moved, "a name change moves no menu target")
	})
}

func TestReferenceUpdateWithoutChangesAuditsNothing(t *testing.T) {
	forEachKind(t, func(t *testing.T, k referenceKind) {
		f, p := newFixture(t, nil, nil), newPrincipal(t)
		description := "same"
		existing := f.store.SeedReference(k.kind, referencecatalog.Reference{Name: "Same", Slug: "same", Description: &description})
		name, slug := "  Same ", "same"
		command := referencecatalog.UpdateReferenceCommand{ID: existing.ID, Name: &name, Slug: &slug}
		if k.described {
			command.Description = &description
		}
		f.store.Fail("UpdateReference", errors.New("an unchanged reference is not written"))

		updated, err := k.update(f.service, p.ctx, command)

		require.NoError(t, err)
		stored, _ := f.store.StoredReference(k.kind, existing.ID)
		require.Equal(t, stored, updated)
		require.Empty(t, f.audit.Records())
		require.Empty(t, f.store.MenuTargetSlugChanges())
		require.Equal(t, mutation.StepCommit, f.lastStep())
	})
}

func TestReferenceDescriptionOfAKindWithoutOneIsInvalid(t *testing.T) {
	f, p := newFixture(t, nil, nil), newPrincipal(t)
	tag := f.store.SeedReference(referencecatalog.KindTag, referencecatalog.Reference{Name: "Tag", Slug: "tag"})
	description := "unsupported"

	_, err := f.service.UpdateTag(p.ctx, referencecatalog.UpdateReferenceCommand{ID: tag.ID, Description: &description})

	require.Equal(t, connect.CodeInvalidArgument, codeOf(err))
	require.Equal(t, "description: is not supported", messageOf(err))
}

func TestReferenceMutationsOfAMissingReferenceAnswerNotFoundBeforeAuthorization(t *testing.T) {
	forEachKind(t, func(t *testing.T, k referenceKind) {
		f, p := newFixture(t, &authztest.Checker{}, nil), newPrincipal(t)
		missing := uuid.NewString()
		name := "Anything"

		_, err := k.update(f.service, p.ctx, referencecatalog.UpdateReferenceCommand{ID: missing, Name: &name})
		require.Equal(t, connect.CodeNotFound, codeOf(err))
		require.Equal(t, k.resource()+" not found: "+missing, messageOf(err))
		err = k.delete(f.service, p.ctx, referencecatalog.DeleteReferenceCommand{ID: missing})
		require.Equal(t, connect.CodeNotFound, codeOf(err))
		require.Empty(t, f.checker.Checks(), "a missing root answers before authorization")
	})
}

func TestReferenceMutationsRequireFreshAdministratorAuthority(t *testing.T) {
	forEachKind(t, func(t *testing.T, k referenceKind) {
		f, p := newFixture(t, &authztest.Checker{}, nil), newPrincipal(t)
		existing := f.store.SeedReference(k.kind, referencecatalog.Reference{Name: "Existing", Slug: "existing"})
		blank := "  "

		_, err := k.create(f.service, p.ctx, referencecatalog.CreateReferenceCommand{Name: blank})
		require.Equal(t, connect.CodePermissionDenied, codeOf(err), "authorization answers before validation")
		require.Equal(t, "admin access required", messageOf(err))
		_, err = k.update(f.service, p.ctx, referencecatalog.UpdateReferenceCommand{ID: existing.ID, Name: &blank})
		require.Equal(t, connect.CodePermissionDenied, codeOf(err))
		require.Equal(t, connect.CodePermissionDenied, codeOf(k.delete(f.service, p.ctx, referencecatalog.DeleteReferenceCommand{ID: existing.ID})))
		_, err = k.listAdmin(f.service, p.ctx, referencecatalog.ReferenceListQuery{})
		require.Equal(t, connect.CodePermissionDenied, codeOf(err))

		_, stillStored := f.store.StoredReference(k.kind, existing.ID)
		require.True(t, stillStored)
		require.Empty(t, f.audit.Records())
		require.Empty(t, f.checker.Applied())
	})
}

func TestReferenceMutationsFenceOutADeactivatedAdministrator(t *testing.T) {
	forEachKind(t, func(t *testing.T, k referenceKind) {
		f, p := newFixture(t, nil, nil), newPrincipal(t)
		existing := f.store.SeedReference(k.kind, referencecatalog.Reference{Name: "Existing", Slug: "existing"})
		f.fence.Deactivate(p.identityID)

		_, err := k.create(f.service, p.ctx, referencecatalog.CreateReferenceCommand{Name: "Fenced"})
		require.Equal(t, connect.CodePermissionDenied, codeOf(err))
		require.Equal(t, "admin access required", messageOf(err))
		require.Equal(t, connect.CodePermissionDenied, codeOf(k.delete(f.service, p.ctx, referencecatalog.DeleteReferenceCommand{ID: existing.ID})))
		require.Empty(t, f.checker.Checks(), "the fence answers before SpiceDB")
		require.Equal(t, []string{p.identityID, p.identityID}, f.fence.Locked())
	})
}

func TestReferenceMutationsRejectAMissingSessionBeforeTheStore(t *testing.T) {
	forEachKind(t, func(t *testing.T, k referenceKind) {
		f := newFixture(t, nil, nil)
		ctx := context.Background()
		name := "Unauthenticated"

		_, err := k.create(f.service, ctx, referencecatalog.CreateReferenceCommand{Name: name})
		require.Equal(t, connect.CodeUnauthenticated, codeOf(err))
		_, err = k.update(f.service, ctx, referencecatalog.UpdateReferenceCommand{ID: uuid.NewString(), Name: &name})
		require.Equal(t, connect.CodeUnauthenticated, codeOf(err))
		require.Equal(t, connect.CodeUnauthenticated, codeOf(k.delete(f.service, ctx, referencecatalog.DeleteReferenceCommand{ID: uuid.NewString()})))
		_, err = k.listAdmin(f.service, ctx, referencecatalog.ReferenceListQuery{})
		require.Equal(t, connect.CodeUnauthenticated, codeOf(err))
		require.Empty(t, f.units.Steps())
	})
}

func TestReferenceDeleteRefusesAReferenceInUseAndRemovesMenuTargets(t *testing.T) {
	forEachKind(t, func(t *testing.T, k referenceKind) {
		f, p := newFixture(t, nil, nil), newPrincipal(t)
		used := f.store.SeedReference(k.kind, referencecatalog.Reference{Name: "Used", Slug: "used"})
		unused := f.store.SeedReference(k.kind, referencecatalog.Reference{Name: "Unused", Slug: "unused"})
		f.store.SeedReferenceUse(k.kind, used.ID, 1)

		err := k.delete(f.service, p.ctx, referencecatalog.DeleteReferenceCommand{ID: used.ID})
		require.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
		require.Equal(t, k.resource()+" is still referenced", messageOf(err))
		_, kept := f.store.StoredReference(k.kind, used.ID)
		require.True(t, kept)

		require.NoError(t, k.delete(f.service, p.ctx, referencecatalog.DeleteReferenceCommand{ID: unused.ID}))
		_, kept = f.store.StoredReference(k.kind, unused.ID)
		require.False(t, kept)
		f.requireAudited(t, p, audited(k.deleted, k.resource(), unused.ID))
		require.Equal(t, [][]policyv1.RelationshipMutation{relationship(t, k.policy.DeletePolicy, unused.ID)}, f.checker.Applied())
		if k.menu {
			require.Equal(t, []referencecatalog.MenuTarget{{LinkType: k.resource(), ID: unused.ID, Slug: "unused"}}, f.store.RemovedMenuTargets())
		} else {
			require.Empty(t, f.store.RemovedMenuTargets())
		}
	})
}

func TestReferenceMutationsRollBackWhenAuditFails(t *testing.T) {
	forEachKind(t, func(t *testing.T, k referenceKind) {
		auditDown := errors.New("audit unavailable")
		f, p := newFixture(t, nil, &audittest.Recorder{Err: auditDown}), newPrincipal(t)
		existing := f.store.SeedReference(k.kind, referencecatalog.Reference{Name: "Existing", Slug: "existing"})
		slug := "moved"

		_, err := k.create(f.service, p.ctx, referencecatalog.CreateReferenceCommand{Name: "Rolled back"})
		require.ErrorIs(t, err, auditDown)
		_, err = k.update(f.service, p.ctx, referencecatalog.UpdateReferenceCommand{ID: existing.ID, Slug: &slug})
		require.ErrorIs(t, err, auditDown)
		require.ErrorIs(t, k.delete(f.service, p.ctx, referencecatalog.DeleteReferenceCommand{ID: existing.ID}), auditDown)

		page, err := k.list(f.service, p.ctx, referencecatalog.ReferenceListQuery{})
		require.NoError(t, err)
		require.Equal(t, []referencecatalog.Reference{existing}, page.Items, "nothing the failed mutations stored remains")
		require.Empty(t, f.store.MenuTargetSlugChanges())
		require.Empty(t, f.store.RemovedMenuTargets())
		require.Empty(t, f.checker.Applied(), "the audit fails before the relationship write")
	})
}

func TestReferenceListsPageByDefaultAndCountUsageForAdministrators(t *testing.T) {
	forEachKind(t, func(t *testing.T, k referenceKind) {
		f, p := newFixture(t, nil, nil), newPrincipal(t)
		first := f.store.SeedReference(k.kind, referencecatalog.Reference{Name: "Alpha", Slug: "alpha"})
		second := f.store.SeedReference(k.kind, referencecatalog.Reference{Name: "Beta", Slug: "beta"})
		f.store.SeedReferenceUse(k.kind, second.ID, 3)

		page, err := k.list(f.service, context.Background(), referencecatalog.ReferenceListQuery{})
		require.NoError(t, err)
		require.Equal(t, []referencecatalog.Reference{first, second}, page.Items)
		require.Equal(t, referencecatalog.Pagination{Total: 2, Limit: 100}, page.Pagination, "the public list needs no session")

		admin, err := k.listAdmin(f.service, p.ctx, referencecatalog.ReferenceListQuery{Limit: 1, Offset: 1})
		require.NoError(t, err)
		require.Equal(t, []referencecatalog.ReferenceUsage{{Reference: second, Count: 3}}, admin.Items)
		require.Equal(t, referencecatalog.Pagination{Total: 2, Limit: 1, Offset: 1}, admin.Pagination)
		admin, err = k.listAdmin(f.service, p.ctx, referencecatalog.ReferenceListQuery{Limit: 1})
		require.NoError(t, err)
		require.Equal(t, []referencecatalog.ReferenceUsage{{Reference: first}}, admin.Items)
		require.True(t, admin.Pagination.HasMore)
		require.Equal(t, []authztest.Check{
			authztest.Of(p.identityID, k.can(t, k.policy.List), nil), authztest.Of(p.identityID, k.can(t, k.policy.List), nil),
		}, f.checker.Checks())

		_, err = k.list(f.service, p.ctx, referencecatalog.ReferenceListQuery{
			Filters: []*commonv1.FilterSpec{{Field: "id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: first.ID}},
		})
		require.Equal(t, connect.CodeInvalidArgument, codeOf(err))
	})
}
