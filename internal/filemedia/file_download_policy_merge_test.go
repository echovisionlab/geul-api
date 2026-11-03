package filemedia

import (
	"context"
	"reflect"
	"testing"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
)

func TestUpdateFileDownloadPolicyRequiresObservedPolicy(t *testing.T) {
	_, err := NewConnectHandler((withTestMutations(&Service{}))).UpdateFileDownloadPolicy(context.Background(), connect.NewRequest(
		&managev1.UpdateFileDownloadPolicyRequest{
			EntityType:     managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
			EntityId:       uuid.NewString(),
			ExpectedFileId: uuid.NewString(),
			Access:         managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_PUBLIC,
		},
	))
	if errs.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("UpdateFileDownloadPolicy() error code = %v, want InvalidArgument; err = %v", errs.CodeOf(err), err)
	}
}

func TestMergeObservedFileDownloadPolicyPreservesIndependentChanges(t *testing.T) {
	currentAccess, criteriaIDs := mergeObservedFileDownloadPolicy(
		mediaasset.FileDownloadAccessRestricted,
		mediaasset.FileDownloadAccessRestricted,
		mediaasset.FileDownloadAccessRestricted,
		[]string{"criteria-a", "criteria-c"},
		[]string{"criteria-a", "criteria-b"},
		[]string{"criteria-b", "criteria-d"},
	)

	if currentAccess != mediaasset.FileDownloadAccessRestricted {
		t.Fatalf("access = %q, want restricted", currentAccess)
	}
	if want := []string{"criteria-c", "criteria-d"}; !reflect.DeepEqual(criteriaIDs, want) {
		t.Fatalf("criteria IDs = %v, want %v", criteriaIDs, want)
	}
}

func TestMergeObservedFileDownloadPolicyAppliesTouchedAccessAndClearsCriteriaSet(t *testing.T) {
	currentAccess, criteriaIDs := mergeObservedFileDownloadPolicy(
		mediaasset.FileDownloadAccessRestricted,
		mediaasset.FileDownloadAccessRestricted,
		mediaasset.FileDownloadAccessPublic,
		[]string{"criteria-a", "criteria-peer"},
		[]string{"criteria-a"},
		nil,
	)

	if currentAccess != mediaasset.FileDownloadAccessPublic {
		t.Fatalf("access = %q, want public", currentAccess)
	}
	if len(criteriaIDs) != 0 {
		t.Fatalf("criteria IDs = %v, want none for non-restricted access", criteriaIDs)
	}
}

func TestMergeObservedFileDownloadPolicyKeepsUntouchedAccess(t *testing.T) {
	currentAccess, _ := mergeObservedFileDownloadPolicy(
		mediaasset.FileDownloadAccessAuthenticated,
		mediaasset.FileDownloadAccessRestricted,
		mediaasset.FileDownloadAccessRestricted,
		[]string{"criteria-a"},
		[]string{"criteria-a"},
		[]string{"criteria-a"},
	)

	if currentAccess != mediaasset.FileDownloadAccessAuthenticated {
		t.Fatalf("access = %q, want current authenticated access", currentAccess)
	}
}
