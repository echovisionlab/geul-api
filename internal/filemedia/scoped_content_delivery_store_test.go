package filemedia_test

import (
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	optional "github.com/echovisionlab/geul-api/internal/optional"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
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
			item := &fileref.BlockMediaItem{Selector: &fileref.BlockMediaSelector{BlockID: block, ReferencePath: "file"}, Attachment: &fileref.Attachment{Kind: fileref.AttachmentActive, FileID: row.ID}, Delivery: &fileref.Delivery{FileID: "stale"}}
			item.Extensions = []byte{0xa0, 0x06, 0x01}
			item.Selector.Extensions = []byte{0xa0, 0x06, 0x02}
			item.Attachment.Extensions = []byte{0xa0, 0x06, 0x03}
			if state == "wrong document" {
				document = uuid.New()
			}
			if state == "wrong path" {
				item.Selector.ReferencePath = "other"
			}
			if state == "read failure" {
				bound.Fail = errors.New("store read failed")
			}
			original := item.Clone()
			got, err := service.HydrateAuthorizedPageBlockMedia(t.Context(), filemedia.Joined{Store: bound}, owner, document, user, []*fileref.BlockMediaItem{nil, item})
			require.Equal(t, original, item)
			if state == "read failure" {
				require.ErrorIs(t, err, bound.Fail)
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, item.Extensions, got[0].Extensions)
			require.NotSame(t, item, got[0])
			require.NotSame(t, item.Selector, got[0].Selector)
			require.NotSame(t, item.Attachment, got[0].Attachment)
			require.Equal(t, item.Selector.Extensions, got[0].Selector.Extensions)
			require.Equal(t, item.Attachment.Extensions, got[0].Attachment.Extensions)
			if state != "current" && state != "uploader" {
				require.Nil(t, got[0].Delivery)
				require.Equal(t, fileref.NoDownload, got[0].DownloadAction)
				return
			}
			require.Equal(t, row.ID, optional.Value(got[0]).Delivery.FileID)
			require.Equal(t, "owner transaction", optional.Value(optional.Value(got[0]).Delivery.FileName))
			require.NotNil(t, optional.Value(got[0]).Delivery.Inline)
			require.Equal(t, state == "uploader", optional.Value(got[0]).Delivery.Download != nil)
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
			require.Equal(t, row.ID, got.FileID)
			require.NotNil(t, got.Inline)
			require.Nil(t, got.Download)
		})
	}
}
