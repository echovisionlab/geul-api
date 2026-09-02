package filemedia

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestContentBlockFileReuseAuthority(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:content-block-file-reuse?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE file (id TEXT PRIMARY KEY, uploaded_by_member_id TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE content_block (id TEXT PRIMARY KEY, document_id TEXT NOT NULL)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE content_block_attachment (block_id TEXT NOT NULL, reference_path TEXT NOT NULL, selector_kind TEXT NOT NULL, file_id TEXT, missing_kind TEXT)`).Error)

	documentID := uuid.New()
	fileID := uuid.New()
	uploaderID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO file (id, uploaded_by_member_id) VALUES (?, ?)`, fileID, uploaderID).Error)
	authorizer := NewContentBlockFileReuseAuthorizer(nil)
	file := contentblock.File{ID: fileID, MIMEType: "image/png"}
	document := contentblock.Document{ID: documentID}

	t.Run("uploader", func(t *testing.T) {
		ctx := contentBlockReuseTestContext(uploaderID)
		require.NoError(t, authorizer.AuthorizeFileReuse(
			ctx, db, document, contentblock.FullBlock{}, contentblock.FileReference{}, file,
		))
	})

	t.Run("existing document reference", func(t *testing.T) {
		blockID := uuid.New()
		require.NoError(t, db.Exec(`INSERT INTO content_block (id, document_id) VALUES (?, ?)`, blockID, documentID).Error)
		require.NoError(t, db.Exec(`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id) VALUES (?, 'primary', 'active', ?)`, blockID, fileID).Error)
		ctx := contentBlockReuseTestContext(uuid.NewString())
		require.NoError(t, authorizer.AuthorizeFileReuse(
			ctx, db, document, contentblock.FullBlock{}, contentblock.FileReference{}, file,
		))
	})

	t.Run("foreign library File", func(t *testing.T) {
		ctx := contentBlockReuseTestContext(uuid.NewString())
		err := authorizer.AuthorizeFileReuse(
			ctx,
			db,
			contentblock.Document{ID: uuid.New()},
			contentblock.FullBlock{},
			contentblock.FileReference{},
			file,
		)
		require.ErrorContains(t, err, "SpiceDB File reuse authorization is not configured")
	})
}

func contentBlockReuseTestContext(memberID string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(uuid.NewString()),
		MemberID:      auth.MemberID(memberID),
		Authenticated: true,
		Onboarded:     true,
	})
}
