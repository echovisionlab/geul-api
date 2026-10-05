package post

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/echovisionlab/geul-api/internal/auth"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

func TestCreateSourceLocaleHonorsExplicitCanonicalLocaleAndRetainsFallback(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	memberID := "11111111-1111-4111-8111-111111111111"
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{Authenticated: true, MemberID: auth.MemberID(memberID)})
	// The explicit locale needs no preference read and must win even when the
	// member prefers ko and the header requests ja.
	locale, err := resolveCreateSourceLocale(ctx, db, nil, "en", "ja")
	if err != nil || locale != "en" {
		t.Fatalf("explicit locale=(%q, %v)", locale, err)
	}
	mock.ExpectQuery(`SELECT "preferred_locale" FROM "member"`).WithArgs(memberID).WillReturnRows(sqlmock.NewRows([]string{"preferred_locale"}).AddRow("ko"))
	locale, err = resolveCreateSourceLocale(ctx, db, nil, "", "ja")
	if err != nil || locale != "ko" {
		t.Fatalf("omitted locale lost preference fallback=(%q, %v)", locale, err)
	}
	locale, err = resolveCreateSourceLocale(t.Context(), nil, nil, "", "ja")
	if err != nil || locale != "ja" {
		t.Fatalf("anonymous header fallback=(%q, %v)", locale, err)
	}
	for _, requested := range []string{"xx", "EN", "en-US", " en "} {
		if _, err := resolveCreateSourceLocale(ctx, db, nil, requested, "ja"); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("unsupported locale %q error=%v", requested, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostDocumentIdentityRequiresExactCanonicalLocale(t *testing.T) {
	postID := uuid.NewString()
	locale, err := validatePostAIDocumentIdentity(postID, "pt-PT")
	require.NoError(t, err)
	require.Equal(t, "pt-PT", locale)
	for _, input := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "pt"} {
		_, err := validatePostAIDocumentIdentity(postID, input)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), input)
	}
}

func TestPostTargetLocaleValueDeleteRequiresInternalReplacementAuthority(t *testing.T) {
	documentID := uuid.New()
	blockID := uuid.New()
	storage := &contentv1.ContentStorageMutationBatch{
		ExpectedRevision: documentID.String(),
		LocaleGroups: []contentv1.ContentStorageLocaleMutationGroup{{
			Locale:  "en",
			Deletes: []string{blockID.String()},
		}},
	}

	require.Error(t, validatePostTargetStorage(storage, documentID, "en", false))
	require.NoError(t, validatePostTargetStorage(storage, documentID, "en", true))

	batch := contentblock.Batch{
		DocumentID:       documentID,
		ExpectedRevision: documentID,
		LocaleGroups: []contentblock.LocaleMutationGroup{{
			Locale:  "en",
			Deletes: []uuid.UUID{blockID},
		}},
	}
	require.Error(t, validatePostTargetBatch(batch, documentID, documentID, "en", false))
	require.NoError(t, validatePostTargetBatch(batch, documentID, documentID, "en", true))
}
