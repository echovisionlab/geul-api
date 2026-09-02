package post

import (
	"reflect"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestPostSystemTranslationDocumentFenceAllowsArchivedRootAndRejectsDeletedRoot(t *testing.T) {
	postID := uuid.NewString()
	documentID := uuid.New()
	rootExists := true
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Callback().Query().Replace("gorm:query", func(query *gorm.DB) {
		switch query.Statement.Table {
		case "post":
			if !rootExists {
				query.RowsAffected = 0
				query.AddError(gorm.ErrRecordNotFound)
				return
			}
			switch root := query.Statement.Dest.(type) {
			case *postContentDocumentRoot:
				*root = postContentDocumentRoot{
					ID:                postID,
					Status:            model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String()),
					ContentDocumentID: &documentID,
					SourceLocale:      "en",
				}
			default:
				destination := reflect.ValueOf(query.Statement.Dest)
				if destination.Kind() != reflect.Pointer || destination.Elem().Kind() != reflect.Struct {
					query.AddError(gorm.ErrInvalidValue)
					return
				}
				field := destination.Elem().FieldByName("SourceLocale")
				if !field.IsValid() || !field.CanSet() {
					query.AddError(gorm.ErrInvalidValue)
					return
				}
				field.SetString("en")
			}
		default:
			query.AddError(gorm.ErrInvalidValue)
			return
		}
		query.RowsAffected = 1
	}))

	domain, err := postSystemTranslationDocumentFence(postID)(t.Context(), db, documentID)
	require.NoError(t, err, "an already-submitted translation job may apply to an archived Post")
	require.Equal(t, "en", domain.SourceLocale)

	_, err = postCreationDocumentFence(postID, "en")(t.Context(), db, documentID)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"ordinary archived Post mutations remain fenced")

	rootExists = false
	_, err = postSystemTranslationDocumentFence(postID)(t.Context(), db, documentID)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"a deleted Post root must reject an existing translation job")
}
