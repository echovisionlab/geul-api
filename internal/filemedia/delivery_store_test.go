package filemedia_test

import (
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/mediaauth"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestManageDeliveryStoreFencesRejectChangedWitnesses(t *testing.T) {
	for _, mode := range []string{"current", "inactive", "uploader", "avatar", "pending", "metadata", "error", "usage", "owner_changed", "relation_changed"} {
		t.Run(mode, func(t *testing.T) {
			f := newLibraryFixture(t)
			row := filemediatest.CatalogFile("original", nil)
			row.MemberID = new(f.user.MemberID.String())
			f.store.Files[row.ID] = filemediatest.CatalogFileRecord{Row: row}
			f.store.AvatarOwners[row.ID] = f.user.IdentityID.String()
			response := &filemedia.MediaDeliveryResult{Delivery: &filemedia.Delivery{FileID: row.ID, FileName: &row.Name, Extension: *row.Extension, MimeType: *row.MimeType, FileSize: *row.FileSize, DurationSeconds: row.DurationSeconds, Inline: &mediaauth.Reference{URL: "old-inline"}, Download: &mediaauth.Reference{URL: "old-download"}}}
			grant := filemedia.ExportDeliveryGrant{Uploader: f.user.MemberID.String(), AvatarOwner: f.user.IdentityID.String()}
			var witness domain.DeliveryUsageWitness
			if mode == "usage" || mode == "owner_changed" || mode == "relation_changed" {
				owner, doc := uuid.NewString(), uuid.NewString()
				witness = domain.DeliveryUsageWitness{Kind: "featured_image", FileID: row.ID, ResourceType: "post", ResourceID: owner}
				f.store.DeliveryOwners["post\x00"+owner] = domain.DeliveryOwner{ID: owner, ContentDocumentID: &doc, Status: "POST_STATUS_PUBLISHED"}
				f.store.ExactUsages[witness.Key()] = true
				grant = filemedia.ExportDeliveryGrant{Usage: &witness, OwnerFingerprint: doc + ":POST_STATUS_PUBLISHED"}
				if mode == "owner_changed" {
					grant.OwnerFingerprint = uuid.NewString() + ":POST_STATUS_PUBLISHED"
				}
				if mode == "relation_changed" {
					delete(f.store.ExactUsages, witness.Key())
				}
			}
			switch mode {
			case "inactive":
				f.store.PrincipalFence.Deactivate(f.user.IdentityID.String())
			case "uploader":
				grant.Uploader = uuid.NewString()
			case "avatar":
				f.store.AvatarOwners[row.ID] = uuid.NewString()
			case "pending":
				record := f.store.Files[row.ID]
				record.PendingDeletion = true
				f.store.Files[row.ID] = record
			case "metadata":
				response.Delivery.FileSize++
			case "error":
				f.store.Fail = errors.New("store failure")
			}
			got, changed, err := filemedia.ExportFinalizeManageDelivery(f.units, f.ctx, f.user, response, grant)
			if mode == "error" {
				require.ErrorIs(t, err, f.store.Fail)
				require.Empty(t, got)
				return
			}
			require.NoError(t, err)
			current := mode == "current" || mode == "usage"
			require.Equal(t, !current, changed)
			if !current {
				require.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			require.NotEqual(t, "old-inline", got[row.ID].Delivery.Inline.URL)
			require.Equal(t, "old-inline", response.Delivery.Inline.URL)
			if mode == "usage" {
				require.Nil(t, got[row.ID].Delivery.Download)
				require.Equal(t, []string{"owner:post\x00" + witness.ResourceID, "principal", "usage:" + witness.Key(), "files"}, f.store.DeliveryLocks)
			} else {
				require.NotEqual(t, "old-download", got[row.ID].Delivery.Download.URL)
				require.Equal(t, []string{"principal", "files", "avatar"}, f.store.DeliveryLocks)
			}
		})
	}
}
