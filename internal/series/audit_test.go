package series_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/series"
	"github.com/echovisionlab/geul-api/internal/series/seriestest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// attributes is the JSON of a record's facet fields, as the durable writer
// stores them.
func attributes(t *testing.T, record sharedtelemetry.AuditRecord) string {
	t.Helper()
	encoded, err := json.Marshal(record)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(encoded, &fields))
	for _, key := range []string{"audit_id", "occurred_at", "action", "request_id", "trace_id", "span_id", "actor_kind", "actor_member_id", "actor_service", "target_type", "target_id"} {
		delete(fields, key)
	}
	encoded, err = json.Marshal(fields)
	require.NoError(t, err)
	return string(encoded)
}

func TestPostSeriesMutationsNameTheirAuditRecords(t *testing.T) {
	f := newFixture(t)
	ctx := f.ctx(t)
	created, err := f.service.CreateSeries(ctx, series.CreateSeriesCommand{Title: "Series audit"})
	require.NoError(t, err)
	seriesID := created.Series.ID
	_, err = f.service.UpdateSeries(ctx, series.UpdateSeriesCommand{
		ID: seriesID, Slug: text("series-audit-updated"), Status: text(string(series.StatusPublished)), Title: text("Series audit source changed"),
	})
	require.NoError(t, err)
	managerMemberID := uuid.NewString()
	f.store.SeedMember(managerMemberID, seriestest.Member{IdentityID: uuid.NewString()})
	require.NoError(t, f.service.AddSeriesManager(ctx, series.AddSeriesManagerCommand{SeriesID: seriesID, MemberID: managerMemberID}))
	posts := f.seedPosts(2)
	for _, postID := range posts {
		require.NoError(t, f.service.AssignPostToSeries(ctx, series.AssignPostToSeriesCommand{PostID: postID, SeriesID: seriesID}))
	}
	reordered := []string{posts[1], posts[0]}
	require.NoError(t, f.service.ReorderSeriesPosts(ctx, series.ReorderSeriesPostsCommand{SeriesID: seriesID, PostIDs: reordered}))
	require.NoError(t, f.service.ReorderSeriesPosts(ctx, series.ReorderSeriesPostsCommand{SeriesID: seriesID, PostIDs: reordered}))
	require.NoError(t, f.service.UnassignPostFromSeries(ctx, series.UnassignPostFromSeriesCommand{SeriesID: seriesID, PostID: posts[0]}))
	require.NoError(t, f.service.RemoveSeriesManager(ctx, series.RemoveSeriesManagerCommand{SeriesID: seriesID, MemberID: managerMemberID}))
	fileID := uuid.NewString()
	f.store.SeedFile(fileID, nil)
	_, err = f.service.SetSeriesFeaturedImage(ctx, series.SetSeriesFeaturedImageCommand{SeriesID: seriesID, FileID: fileID})
	require.NoError(t, err)
	_, err = f.service.SetSeriesFeaturedImage(ctx, series.SetSeriesFeaturedImageCommand{SeriesID: seriesID, FileID: fileID})
	require.NoError(t, err)
	_, err = f.service.DeleteSeriesFeaturedImage(ctx, series.DeleteSeriesFeaturedImageCommand{SeriesID: seriesID})
	require.NoError(t, err)
	f.spiceDB.Relationships[seriesID] = []policyv1.RelationshipMutation{must(policyv1.PostSeries.TouchPolicy(seriesID))}
	require.NoError(t, f.service.DeleteSeries(ctx, series.DeleteSeriesCommand{ID: seriesID}))

	records := f.audit.Records()
	want := []struct {
		action     sharedtelemetry.AuditAction
		attributes string
	}{
		{sharedtelemetry.AuditPostSeriesCreated, `{}`},
		{sharedtelemetry.AuditPostSeriesUpdated, `{"changed_fields":["slug","source_copy"]}`},
		{sharedtelemetry.AuditPostSeriesUpdated, `{"changed_fields":["status"],"previous_state":"draft","new_state":"published"}`},
		{sharedtelemetry.AuditPostSeriesUpdated, `{"changed_fields":["managers"],"subject_member_id":"` + managerMemberID + `","previous_relationship":"none","new_relationship":"manager"}`},
		{sharedtelemetry.AuditPostSeriesUpdated, `{"changed_fields":["posts"],"subject_post_id":"` + posts[0] + `","new_series_id":"` + seriesID + `"}`},
		{sharedtelemetry.AuditPostSeriesUpdated, `{"changed_fields":["posts"],"subject_post_id":"` + posts[1] + `","new_series_id":"` + seriesID + `"}`},
		{sharedtelemetry.AuditPostSeriesUpdated, `{"changed_fields":["post_order"],"post_ids":["` + posts[1] + `","` + posts[0] + `"]}`},
		{sharedtelemetry.AuditPostSeriesUpdated, `{"changed_fields":["posts"],"subject_post_id":"` + posts[0] + `","previous_series_id":"` + seriesID + `"}`},
		{sharedtelemetry.AuditPostSeriesUpdated, `{"changed_fields":["managers"],"subject_member_id":"` + managerMemberID + `","previous_relationship":"manager","new_relationship":"none"}`},
		{sharedtelemetry.AuditPostSeriesUpdated, `{"changed_fields":["featured_image"],"collection_operation":"added","file_id":"` + fileID + `"}`},
		{sharedtelemetry.AuditPostSeriesUpdated, `{"changed_fields":["featured_image"],"collection_operation":"removed","file_id":"` + fileID + `"}`},
		{sharedtelemetry.AuditPostSeriesDeleted, `{}`},
	}
	require.Len(t, records, len(want))
	for index, record := range records {
		require.Equal(t, want[index].action, record.Action, "record %d", index)
		require.Equal(t, "post_series", record.TargetType)
		require.Equal(t, seriesID, record.TargetID)
		require.Equal(t, f.memberID, record.MemberID)
		require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), record.RequestID)
		require.JSONEq(t, want[index].attributes, attributes(t, record), "record %d", index)
	}
}
