//go:build integration

package filemedia

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestContentBlockForeignFileReuseUsesFileListAdmissionIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	fileID := uuid.New()
	uploaderIdentityID := uuid.NewString()
	uploaderMemberID := seedExternalKratosIdentityWithTraits(
		t,
		db,
		uploaderIdentityID,
		"File reuse uploader",
	)
	require.NoError(t, db.Exec(
		`INSERT INTO file (id, file_name, mime_type, file_size, extension, uploaded_by_member_id) VALUES (?, 'shared', 'image/png', 1, 'png', ?)`,
		fileID,
		uploaderMemberID,
	).Error)

	spiceDB := integrationSpiceDB(t)
	authorizer := NewContentBlockFileReuseAuthorizer(spiceDB)
	file := contentblock.File{ID: fileID, MIMEType: "image/png"}

	tests := []struct {
		name    string
		role    policyv1.RoleID
		wantErr bool
	}{
		{name: "author", role: policyv1.Role.Author()},
		{name: "ordinary user", role: policyv1.Role.User(), wantErr: true},
		{name: "admin inherits author admission", role: policyv1.Role.Admin()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identityID := uuid.NewString()
			memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, test.name)
			grantIntegrationGlobalRole(t, spiceDB, identityID, test.role)
			ctx := auth.WithUser(context.Background(), &auth.UserInfo{
				IdentityID:    auth.IdentityID(identityID),
				MemberID:      auth.MemberID(memberID),
				SessionID:     auth.SessionID(uuid.NewString()),
				Authenticated: true,
				Onboarded:     true,
			})

			// File reuse admission is independent of document edit authority.
			// The owning domain checks that authority at its locked mutation boundary.
			err := authorizer.AuthorizeFileReuse(
				ctx,
				db,
				contentblock.Document{ID: uuid.New()},
				contentblock.FullBlock{},
				contentblock.FileReference{},
				file,
			)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
