package filemedia_test

import (
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestScopedContentDeliveryUsesJoinedStoreAndExactReferences(t *testing.T) {
	for _, state := range []string{"current", "uploader", "replaced", "wrong document", "wrong path", "deleting", "read failure"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			bound := filemediatest.NewLibraryStore()
			global := filemediatest.NewLibraryStore()
			global.Fail = errors.New("must use owner transaction")
			service := filemedia.ExportDeliveryService(mutationtest.New[filemedia.Store](global))
			owner, document, block := uuid.NewString(), uuid.New(), uuid.NewString()
			user := &auth.UserInfo{Authenticated: true, MemberID: auth.MemberID(uuid.NewString())}
			row := filemediatest.CatalogFile("owner transaction", nil)
			if state == "uploader" {
				row.MemberID = new(user.MemberID.String())
			}
			bound.Files[row.ID] = filemediatest.CatalogFileRecord{Row: row, PendingDeletion: state == "deleting"}
			bound.ScopedOwners["page:"+owner] = filemediatest.ScopedOwner{DocumentID: document.String()}
			attachment := domain.ScopedAttachment{BlockID: block, ReferencePath: "file", FileID: row.ID}
			if state == "replaced" {
				attachment.FileID = uuid.NewString()
			}
			bound.ScopedAttachments = []filemediatest.ScopedAttachmentRecord{{OwnerKind: "page", OwnerID: owner, DocumentID: document.String(), Attachment: attachment}}
			item := &contentv1.ContentBlockMediaItem{Selector: &contentv1.ContentBlockMediaSelector{BlockId: block, ReferencePath: "file"}, Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: row.ID}}, Delivery: &commonv1.MediaDelivery{FileId: "stale"}}
			item.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
			if state == "wrong document" {
				document = uuid.New()
			}
			if state == "wrong path" {
				item.Selector.ReferencePath = "other"
			}
			if state == "read failure" {
				bound.Fail = errors.New("store read failed")
			}
			original := proto.Clone(item)
			got, err := service.HydrateAuthorizedPageBlockMedia(t.Context(), filemedia.Joined{Store: bound}, owner, document, user, []*contentv1.ContentBlockMediaItem{nil, item})
			require.True(t, proto.Equal(original, item))
			if state == "read failure" {
				require.ErrorIs(t, err, bound.Fail)
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, item.ProtoReflect().GetUnknown(), got[0].ProtoReflect().GetUnknown())
			if state != "current" && state != "uploader" {
				require.Nil(t, got[0].Delivery)
				require.Equal(t, contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_NONE, got[0].DownloadAction)
				return
			}
			require.Equal(t, row.ID, got[0].GetDelivery().GetFileId())
			require.Equal(t, "owner transaction", got[0].GetDelivery().GetFileName())
			require.NotNil(t, got[0].GetDelivery().GetInline())
			require.Equal(t, state == "uploader", got[0].GetDelivery().GetDownload() != nil)
		})
	}
}

func TestScopedFeaturedImageUsesOwnerAuthorizationBeforeSlotRead(t *testing.T) {
	for _, state := range []string{"current", "changed", "missing", "deleting", "denied"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			store := filemediatest.NewLibraryStore()
			service := filemedia.ExportDeliveryService(mutationtest.New[filemedia.Store](store))
			owner, doc := uuid.NewString(), uuid.NewString()
			row := filemediatest.CatalogFile("featured", nil)
			store.Files[row.ID] = filemediatest.CatalogFileRecord{Row: row, PendingDeletion: state == "deleting"}
			if state != "missing" {
				store.ScopedOwners["post:"+owner] = filemediatest.ScopedOwner{FeaturedFileID: &row.ID, DocumentID: doc}
			}
			expected := row.ID
			if state == "changed" {
				expected = uuid.NewString()
			}
			denied := errors.New("owner denied")
			if state == "denied" {
				store.PolicyDenied["post\x00"+owner] = denied
			}
			got, err := service.ResolveAuthorizedPostFeaturedImage(t.Context(), owner, expected)
			if state == "missing" || state == "denied" {
				require.Error(t, err)
				require.Nil(t, got)
				if state == "denied" {
					require.ErrorIs(t, err, denied)
				}
				return
			}
			require.NoError(t, err)
			if state != "current" {
				require.Nil(t, got)
				return
			}
			require.Equal(t, row.ID, got.FileId)
			require.NotNil(t, got.Inline)
			require.Nil(t, got.Download)
		})
	}
}
