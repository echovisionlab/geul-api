package filemedia

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/testutil/sqlitetest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestContentBlockFileReuseAuthority(t *testing.T) {
	t.Parallel()
	db, err := sqlitetest.Open(t, sqlite.Open, &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE file ( client_media_bundle_id TEXT,id TEXT PRIMARY KEY, uploaded_by_member_id TEXT)`).Error)
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

// A foreign library File is admitted by File list authority alone; the owning
// domain checks document edit authority at its locked mutation boundary.
func TestContentBlockForeignFileReuseUsesFileListAdmission(t *testing.T) {
	t.Parallel()
	db, err := sqlitetest.Open(t, sqlite.Open, &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE file ( client_media_bundle_id TEXT,id TEXT PRIMARY KEY, uploaded_by_member_id TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE content_block (id TEXT PRIMARY KEY, document_id TEXT NOT NULL)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE content_block_attachment (block_id TEXT NOT NULL, reference_path TEXT NOT NULL, selector_kind TEXT NOT NULL, file_id TEXT, missing_kind TEXT)`).Error)
	fileID := uuid.New()
	require.NoError(t, db.Exec(`INSERT INTO file (id, uploaded_by_member_id) VALUES (?, ?)`, fileID, uuid.NewString()).Error)
	file := contentblock.File{ID: fileID, MIMEType: "image/png"}

	for _, test := range []struct {
		name    string
		checker *authztest.Checker
		wantErr bool
	}{
		{name: "File list allowed", checker: (&authztest.Checker{}).Allow(policyv1.File.List())},
		{name: "File list denied", checker: &authztest.Checker{}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			authorizer := NewContentBlockFileReuseAuthorizer(test.checker.Client(t))
			err := authorizer.AuthorizeFileReuse(
				auditedUserContext(context.Background(), newUnitUser().AuthUserInfo()),
				db,
				contentblock.Document{ID: uuid.New()},
				contentblock.FullBlock{},
				contentblock.FileReference{},
				file,
			)
			if test.wantErr {
				require.Equal(t, connect.CodePermissionDenied, errs.CodeOf(err))
				return
			}
			require.NoError(t, err)
		})
	}
}

func contentBlockReuseTestContext(memberID string) context.Context {
	return auditedUserContext(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(uuid.NewString()),
		MemberID:      auth.MemberID(memberID),
		Authenticated: true,
		Onboarded:     true,
	})
}
