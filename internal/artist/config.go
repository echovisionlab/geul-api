package artist

import (
	"fmt"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

var ArtistFilterConfig = &queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search":       {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{ArtistSourceTitleSQL("artist"), "real_name"}},
	"country_code": {Column: "country_code", Type: queryutil.TypeText, AllowedOps: queryutil.TextOps},
	"status": {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
		managev1.ArtistStatus_ARTIST_STATUS_DRAFT.String(), managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String(),
	}},
}}

func validateResourceDeletionAuthorizationBatchSize(resourceName string, deleted, restored []policyv1.RelationshipMutation) error {
	const maxMutations = 1000
	if len(deleted) <= maxMutations && len(restored) <= maxMutations {
		return nil
	}
	return errs.FailedPrecondition(fmt.Sprintf("%s has too many authorization relationships to delete atomically; remove participant relationships or reparent dependent resources first", resourceName))
}
