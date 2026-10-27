package sitesettings

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Every writable setting key must map to an audit field that the shared
// telemetry contract accepts; otherwise the audit build fails and the whole
// settings change rolls back.
func TestSiteSettingAuditFieldsAcceptEveryKey(t *testing.T) {
	keys := []string{"og_image_config.home", "og_image_config.content"}
	for key := range defaultSettings {
		keys = append(keys, key)
	}
	metadata := sharedtelemetry.AuditMetadata{
		AuditID:     uuid.NewString(),
		OccurredAt:  time.Now().UTC(),
		Correlation: sharedtelemetry.Correlation{RequestID: uuid.NewString()},
		RecordActor: sharedtelemetry.RecordActor{Kind: sharedtelemetry.ActorKindMember, MemberID: uuid.NewString()},
	}
	for _, key := range keys {
		_, err := sharedtelemetry.NewSiteSettingsUpdatedAuditRecord(metadata, siteSettingAuditChangedFields([]string{key}))
		require.NoError(t, err, key)
	}
}
