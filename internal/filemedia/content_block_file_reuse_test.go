package filemedia_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestContentBlockFileReuseAuthority(t *testing.T) {
	for _, state := range []string{"uploader", "document reference", "library allowed", "library denied", "unconfigured", "dependency failed", "anonymous", "banned", "read failed"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			f := newLibraryFixture(t)
			doc := uuid.NewString()
			row := filemediatest.CatalogFile("file", nil)
			row.MemberID = new(uuid.NewString())
			if state == "uploader" {
				row.MemberID = new(f.user.MemberID.String())
			}
			f.store.Files[row.ID] = filemediatest.CatalogFileRecord{Row: row}
			if state == "document reference" {
				f.store.ScopedAttachments = []filemediatest.ScopedAttachmentRecord{{DocumentID: doc, Attachment: domain.ScopedAttachment{FileID: row.ID}}}
			}
			var checker authz.AuthorizationDecisionChecker = (&authztest.Checker{}).Allow(policyv1.File.List())
			var failure error
			switch state {
			case "unconfigured":
				checker = nil
			case "library denied":
				checker = &authztest.Checker{}
			case "dependency failed":
				failure = errors.New("dependency failed")
				checker = &reuseFailChecker{failure}
			case "anonymous":
				f.ctx = auth.WithUser(f.ctx, nil)
			case "banned":
				f.user.Banned = true
			case "read failed":
				failure = errors.New("read failed")
				f.store.Fail = failure
			}
			err := filemedia.AuthorizeContentBlockFileReuse(f.ctx, filemedia.Joined{Store: f.store}, checker, doc, row.ID)
			switch state {
			case "uploader", "document reference", "library allowed":
				require.NoError(t, err)
			case "library denied", "banned":
				require.Equal(t, connect.CodePermissionDenied, errs.CodeOf(err))
			case "anonymous":
				require.Equal(t, connect.CodeUnauthenticated, errs.CodeOf(err))
			case "unconfigured":
				require.ErrorContains(t, err, "SpiceDB File reuse authorization is not configured")
			default:
				require.ErrorIs(t, err, failure)
			}
		})
	}
}

type reuseFailChecker struct{ err error }

func (c *reuseFailChecker) Can(_ context.Context, _ policyv1.AuthorizationDecision) (bool, error) {
	return false, c.err
}
