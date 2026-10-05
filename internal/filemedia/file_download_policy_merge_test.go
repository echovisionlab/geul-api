package filemedia

import (
	"context"
	"reflect"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
)

func observedFileDownloadPolicy(audience managev1.FileDownloadAudience, segmentIDs ...string) *managev1.FileDownloadPolicyObservedState {
	return &managev1.FileDownloadPolicyObservedState{
		Audience:           audience,
		AudienceSegmentIds: segmentIDs,
	}
}

func TestUpdateFileDownloadPolicyRequiresObservedPolicy(t *testing.T) {
	_, err := (&FileService{}).UpdateFileDownloadPolicy(context.Background(), connect.NewRequest(
		&managev1.UpdateFileDownloadPolicyRequest{
			EntityType:     managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
			EntityId:       uuid.NewString(),
			ExpectedFileId: uuid.NewString(),
			Audience:       managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_PUBLIC,
		},
	))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("UpdateFileDownloadPolicy() error code = %v, want InvalidArgument; err = %v", connect.CodeOf(err), err)
	}
}

func TestMergeObservedFileDownloadPolicyPreservesIndependentChanges(t *testing.T) {
	currentAudience, segmentIDs := mergeObservedFileDownloadPolicy(
		mediaasset.FileDownloadAudienceRestricted,
		mediaasset.FileDownloadAudienceRestricted,
		mediaasset.FileDownloadAudienceRestricted,
		[]string{"segment-a", "segment-c"},
		[]string{"segment-a", "segment-b"},
		[]string{"segment-b", "segment-d"},
	)

	if currentAudience != mediaasset.FileDownloadAudienceRestricted {
		t.Fatalf("audience = %q, want restricted", currentAudience)
	}
	if want := []string{"segment-c", "segment-d"}; !reflect.DeepEqual(segmentIDs, want) {
		t.Fatalf("segment IDs = %v, want %v", segmentIDs, want)
	}
}

func TestMergeObservedFileDownloadPolicyAppliesTouchedAudienceAndClearsSegments(t *testing.T) {
	currentAudience, segmentIDs := mergeObservedFileDownloadPolicy(
		mediaasset.FileDownloadAudienceRestricted,
		mediaasset.FileDownloadAudienceRestricted,
		mediaasset.FileDownloadAudiencePublic,
		[]string{"segment-a", "segment-peer"},
		[]string{"segment-a"},
		nil,
	)

	if currentAudience != mediaasset.FileDownloadAudiencePublic {
		t.Fatalf("audience = %q, want public", currentAudience)
	}
	if len(segmentIDs) != 0 {
		t.Fatalf("segment IDs = %v, want none for non-restricted audience", segmentIDs)
	}
}

func TestMergeObservedFileDownloadPolicyKeepsUntouchedAudience(t *testing.T) {
	currentAudience, _ := mergeObservedFileDownloadPolicy(
		mediaasset.FileDownloadAudienceAuthenticated,
		mediaasset.FileDownloadAudienceRestricted,
		mediaasset.FileDownloadAudienceRestricted,
		[]string{"segment-a"},
		[]string{"segment-a"},
		[]string{"segment-a"},
	)

	if currentAudience != mediaasset.FileDownloadAudienceAuthenticated {
		t.Fatalf("audience = %q, want current authenticated audience", currentAudience)
	}
}
