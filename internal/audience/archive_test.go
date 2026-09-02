package audience

import (
	"testing"
	"time"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func TestDownloadPolicyArchiveAuditUsesExactOwningRelation(t *testing.T) {
	metadata := sharedtelemetry.AuditMetadata{
		AuditID:    "00000000-0000-4000-8000-000000000001",
		OccurredAt: time.Date(2026, time.August, 28, 0, 0, 0, 0, time.UTC),
		RecordActor: sharedtelemetry.RecordActor{
			Kind:     sharedtelemetry.ActorKindMember,
			MemberID: "member-1",
		},
	}
	tests := []struct {
		name       string
		domain     string
		action     sharedtelemetry.AuditAction
		targetType string
	}{
		{name: "Post File Block", domain: "post", action: sharedtelemetry.AuditPostUpdated, targetType: "post"},
		{name: "Page File Block", domain: "page", action: sharedtelemetry.AuditPageUpdated, targetType: "page"},
		{name: "Work File Block", domain: "work", action: sharedtelemetry.AuditWorkUpdated, targetType: "work"},
		{name: "Program Event File Block", domain: "program_event", action: sharedtelemetry.AuditProgramEventUpdated, targetType: "program_event"},
		{name: "Release Track", domain: "release", action: sharedtelemetry.AuditReleaseUpdated, targetType: "release"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := downloadPolicyArchivePlan{
				domain:   test.domain,
				ownerID:  "owner-1",
				itemID:   "item-1",
				fileID:   "file-1",
				audience: string(sharedtelemetry.AuditStateRestricted),
			}
			action, build, err := downloadPolicyArchiveAudit(
				plan,
				[]string{"segment-1", "segment-2"},
				[]string{"segment-2"},
			)
			require.NoError(t, err)
			require.Equal(t, test.action, action)
			record, err := build(metadata)
			require.NoError(t, err)
			require.Equal(t, test.action, record.Action)
			require.Equal(t, test.targetType, record.TargetType)
			require.Equal(t, "owner-1", record.TargetID)
			require.Equal(t, "item-1", record.ItemID)
			require.Equal(t, "file-1", record.FileID)
			require.Equal(t, []string{"file_download_audience_segment_ids"}, record.ChangedFields)
			require.Equal(t, &[]string{"segment-1", "segment-2"}, record.PreviousItemIDs)
			require.Equal(t, &[]string{"segment-2"}, record.ItemIDs)
		})
	}
}

func TestDownloadPolicyArchiveAuditRejectsUnsupportedOwner(t *testing.T) {
	_, _, err := downloadPolicyArchiveAudit(downloadPolicyArchivePlan{domain: "file"}, nil, nil)
	require.Error(t, err)
}
