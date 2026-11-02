//go:build integration

package contenttest

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// ContentUpdatedEvents decodes the durable content.updated outbox messages for
// one entity in send order. Tests that need it compose the real PGMQ
// publisher so the outbox rows are the ones production commits.
func ContentUpdatedEvents(t *testing.T, db *gorm.DB, entityID string) []*managev1.ContentUpdatedEvent {
	t.Helper()
	var messages []string
	require.NoError(t, db.Raw(fmt.Sprintf(
		`SELECT message::text FROM pgmq.%s ORDER BY msg_id`,
		pq.QuoteIdentifier("q_"+eventpkg.QueueContentUpdatedOutbox),
	)).Scan(&messages).Error)
	var events []*managev1.ContentUpdatedEvent
	for _, message := range messages {
		var envelope eventpkg.Envelope
		require.NoError(t, json.Unmarshal([]byte(message), &envelope))
		body, err := envelope.Payload()
		require.NoError(t, err)
		event := &managev1.ContentUpdatedEvent{}
		require.NoError(t, proto.Unmarshal(body, event))
		if event.GetEntityId() == entityID {
			events = append(events, event)
		}
	}
	return events
}

// ContentDocumentRows is the stored state of one content document: its CAS
// revision and every block and block-locale row, without timestamps.
type ContentDocumentRows struct {
	Revision string
	Blocks   []ContentBlockRow
	Locales  []ContentBlockLocaleRow
}

type ContentBlockRow struct {
	ID            string
	ParentBlockID *string
	ContainerSlot string
	Position      int
	Kind          string
	SharedData    string
}

type ContentBlockLocaleRow struct {
	BlockID       string
	Locale        string
	LocalizedData string
}

func LoadContentDocumentRows(t *testing.T, db *gorm.DB, documentID string) ContentDocumentRows {
	t.Helper()
	var rows ContentDocumentRows
	require.NoError(t, db.Raw(`SELECT revision::text FROM content_document WHERE id = ?::uuid`, documentID).
		Scan(&rows.Revision).Error)
	require.NotEmpty(t, rows.Revision)
	require.NoError(t, db.Raw(`
		SELECT id::text AS id, parent_block_id::text AS parent_block_id, container_slot,
		       position, kind, shared_data::text AS shared_data
		FROM content_block WHERE document_id = ?::uuid
		ORDER BY parent_block_id NULLS FIRST, container_slot, position, id
	`, documentID).Scan(&rows.Blocks).Error)
	require.NoError(t, db.Raw(`
		SELECT l.block_id::text AS block_id, l.locale, l.localized_data::text AS localized_data
		FROM content_block_locale l JOIN content_block b ON b.id = l.block_id
		WHERE b.document_id = ?::uuid
		ORDER BY l.block_id, l.locale
	`, documentID).Scan(&rows.Locales).Error)
	return rows
}

// LocaleMetadataRow is one <entity>_translation row as stored.
type LocaleMetadataRow struct {
	Locale    string
	Title     *string
	Summary   *string
	UpdatedAt time.Time
}

// LoadLocaleMetadataRows returns every locale row of an entity translation
// table, ordered by locale, with timestamps at PostgreSQL precision.
func LoadLocaleMetadataRows(t *testing.T, db *gorm.DB, table, entityID string) []LocaleMetadataRow {
	t.Helper()
	var rows []LocaleMetadataRow
	require.NoError(t, db.Table(table).Select("locale, title, summary, updated_at").
		Where("entity_id = ?", entityID).Order("locale").Scan(&rows).Error)
	for index := range rows {
		rows[index].UpdatedAt = rows[index].UpdatedAt.UTC().Truncate(time.Microsecond)
	}
	return rows
}

// DomainAuditRow is the observable part of one domain_audit record.
type DomainAuditRow struct {
	Action        string
	ActorKind     string
	ActorMemberID *string
	ChangedFields pq.StringArray `gorm:"type:text[]"`
	VersionID     *string
}

func LoadDomainAuditRows(t *testing.T, db *gorm.DB, targetType, targetID string) []DomainAuditRow {
	t.Helper()
	var rows []DomainAuditRow
	require.NoError(t, db.Raw(`
		SELECT action, actor_kind, actor_member_id::text AS actor_member_id,
		       ARRAY(SELECT jsonb_array_elements_text(attributes->'changed_fields')) AS changed_fields,
		       attributes->>'version_id' AS version_id
		FROM public.domain_audit
		WHERE target_type = ? AND target_id = ?
		ORDER BY occurred_at, audit_id
	`, targetType, targetID).Scan(&rows).Error)
	return rows
}

// CountOgGenerationsForReason counts OG generations queued for an entity by
// one automatic refresh reason.
func CountOgGenerationsForReason(t *testing.T, db *gorm.DB, entityID, reason string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`
		SELECT count(*) FROM og_generation g
		JOIN og_generation_run r ON r.id = g.run_id
		JOIN og_generation_target target ON target.id = g.target_id
		WHERE target.entity_id = ? AND r.reason = ?
	`, entityID, reason).Scan(&count).Error)
	return count
}
