package filemediatest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func policyContractCases() map[string]func(*testing.T, CatalogHarness) {
	cases := map[string]func(*testing.T, CatalogHarness){}
	for _, kind := range []string{"post", "track"} {
		cases["policy "+kind+" relation update and rollback"] = func(t *testing.T, h CatalogHarness) {
			relation := seedContractPolicy(t, h, kind)
			a, b := domain.PolicyCriteria{ID: uuid.NewString(), Name: "Zulu", Kind: "MEMBER_CRITERIA_KIND_MEMBER_TAGS", Eligible: true}, domain.PolicyCriteria{ID: uuid.NewString(), Name: "alpha", Kind: "MEMBER_CRITERIA_KIND_FILTER", Eligible: true}
			h.SeedCriteria(t, a)
			h.SeedCriteria(t, b)
			ids := []string{a.ID, b.ID}
			slices.Sort(ids)
			abort := errors.New("abort")
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.ResolveDownloadPolicyRelation(ctx, relation.Selector, true)
				require.NoError(t, err)
				require.Equal(t, relation, got)
				missing := relation.Selector
				missing.EntityID = uuid.NewString()
				_, err = s.ResolveDownloadPolicyRelation(ctx, missing, false)
				require.ErrorIs(t, err, domain.ErrPolicyRelationNotFound)

				require.NoError(t, s.ValidatePolicyCriteriaSet(ctx, ids))
				require.NoError(t, s.SetDownloadPolicy(ctx, relation.Selector, mediaasset.FileDownloadAccessRestricted, ids, time.Now().UTC()))
				got, err = s.ResolveDownloadPolicyRelation(ctx, relation.Selector, false)
				require.NoError(t, err)
				require.Equal(t, mediaasset.FileDownloadAccessRestricted, got.Access)
				actual, err := s.DownloadPolicyCriteriaIDs(ctx, relation.Selector)
				require.NoError(t, err)
				require.Equal(t, ids, actual)
				return abort
			}), abort)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.ResolveDownloadPolicyRelation(ctx, relation.Selector, false)
				require.NoError(t, err)
				require.Equal(t, mediaasset.FileDownloadAccessDisabled, got.Access)
				actual, err := s.DownloadPolicyCriteriaIDs(ctx, relation.Selector)
				require.NoError(t, err)
				require.Empty(t, actual)
				rows, err := s.ActivePolicyCriteriaSet(ctx, []string{a.ID, b.ID})
				require.NoError(t, err)
				require.Len(t, rows, 2)
				require.Equal(t, []string{b.ID, a.ID}, []string{rows[0].ID, rows[1].ID})
				return nil
			}))
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				return s.SetDownloadPolicy(ctx, relation.Selector, mediaasset.FileDownloadAccessRestricted, ids, time.Now().UTC())
			}))
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				return s.SetDownloadPolicy(ctx, relation.Selector, mediaasset.FileDownloadAccessPublic, nil, time.Now().UTC())
			}))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.ResolveDownloadPolicyRelation(ctx, relation.Selector, false)
				require.NoError(t, err)
				require.Equal(t, mediaasset.FileDownloadAccessPublic, got.Access)
				actual, err := s.DownloadPolicyCriteriaIDs(ctx, relation.Selector)
				require.Empty(t, actual)
				return err
			}))
		}
		cases["policy "+kind+" lock blocks a second update"] = func(t *testing.T, h CatalogHarness) {
			relation := seedContractPolicy(t, h, kind)
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.ResolveDownloadPolicyRelation(ctx, relation.Selector, true)
				return err
			}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error {
				return s.SetDownloadPolicy(ctx, relation.Selector, mediaasset.FileDownloadAccessPublic, nil, time.Now().UTC())
			}))
		}
	}
	cases["policy validation rejects archived ineligible and missing Member criteria"] = func(t *testing.T, h CatalogHarness) {
		now := time.Now().UTC()
		valid := domain.PolicyCriteria{ID: uuid.NewString(), Name: "valid", Kind: "MEMBER_CRITERIA_KIND_MEMBER_TAGS", Eligible: true}
		archived := domain.PolicyCriteria{ID: uuid.NewString(), Name: "archived", Kind: "MEMBER_CRITERIA_KIND_MEMBER_TAGS", Eligible: true, ArchivedAt: &now}
		ineligible := domain.PolicyCriteria{ID: uuid.NewString(), Name: "all", Kind: "MEMBER_CRITERIA_KIND_ALL_MEMBERS", Eligible: false}
		for _, criteria := range []domain.PolicyCriteria{valid, archived, ineligible} {
			h.SeedCriteria(t, criteria)
		}
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
			return s.ValidatePolicyCriteriaSet(ctx, []string{valid.ID})
		}))
		for _, id := range []string{archived.ID, ineligible.ID, uuid.NewString()} {
			require.Error(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error { return s.ValidatePolicyCriteriaSet(ctx, []string{id}) }))
		}
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
			rows, err := s.ActivePolicyCriteriaSet(ctx, []string{valid.ID, archived.ID, ineligible.ID})
			require.NoError(t, err)
			require.Len(t, rows, 2)
			return nil
		}))
	}
	return cases
}
func seedContractPolicy(t *testing.T, h CatalogHarness, kind string) domain.PolicyRelation {
	t.Helper()
	file := CatalogFile("policy", nil)
	h.SeedFile(t, file, false, false)
	selector := domain.PolicySelector{OwnerKind: kind, EntityID: uuid.NewString()}
	resourceID, resourceType := selector.EntityID, kind
	if kind == "track" {
		resourceID, resourceType = uuid.NewString(), "release"
	} else {
		selector.BlockID = new(uuid.NewString())
		selector.ReferencePath = new("file")
	}
	relation := domain.PolicyRelation{Selector: selector, FileID: file.ID, Access: mediaasset.FileDownloadAccessDisabled, ResourceType: resourceType, ResourceID: resourceID}
	h.SeedPolicy(t, relation, nil)
	return relation
}
