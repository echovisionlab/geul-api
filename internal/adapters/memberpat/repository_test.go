package memberpat

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/adapters/patauth"
	patdomain "github.com/echovisionlab/geul-api/internal/member/pat"
	"github.com/echovisionlab/geul-api/internal/testutil/sqlitetest"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRepositoryPersistsMemberScopedCurrentCredentialLifecycle(t *testing.T) {
	t.Parallel()

	db := newPATRepositoryTestDB(t)
	repository := mustPATRepository(t, db)
	createdAt := time.Date(2026, time.August, 23, 1, 2, 3, 0, time.UTC)
	first := storedToken(t, 1, "member-1", createdAt)
	second := storedToken(t, 2, "member-2", createdAt.Add(time.Second))

	if err := repository.Create(t.Context(), first); err != nil {
		t.Fatalf("Create(first) error = %v", err)
	}
	if err := repository.Create(t.Context(), second); err != nil {
		t.Fatalf("Create(second) error = %v", err)
	}
	duplicateMember := storedToken(t, 3, "member-1", createdAt.Add(2*time.Second))
	if err := repository.Create(t.Context(), duplicateMember); !errors.Is(err, patdomain.ErrTokenAlreadyExists) {
		t.Fatalf("Create(duplicate Member) error = %v", err)
	}

	listed, err := repository.ListByMember(t.Context(), first.MemberID)
	if err != nil {
		t.Fatalf("ListByMember() error = %v", err)
	}
	if len(listed) != 1 || !storedTokenEqual(listed[0], first) {
		t.Fatalf("ListByMember() = %+v, want only first token", listed)
	}

	loaded, err := repository.FindByID(t.Context(), first.ID)
	if err != nil || !storedTokenEqual(loaded, first) {
		t.Fatalf("FindByID() = %+v, %v", loaded, err)
	}

	newVerifier := verifier(t, 9)
	updatedAt := createdAt.Add(time.Hour)
	if _, err := repository.ReplaceVerifier(t.Context(), "member-2", first.ID, newVerifier, updatedAt); !errors.Is(err, patdomain.ErrTokenNotFound) {
		t.Fatalf("ReplaceVerifier(other Member) error = %v, want ErrTokenNotFound", err)
	}
	unchanged, err := repository.FindByID(t.Context(), first.ID)
	if err != nil || !bytes.Equal(unchanged.Verifier.Bytes(), first.Verifier.Bytes()) {
		t.Fatalf("other-Member replacement changed verifier: %+v, %v", unchanged, err)
	}

	replaced, err := repository.ReplaceVerifier(t.Context(), first.MemberID, first.ID, newVerifier, updatedAt)
	if err != nil {
		t.Fatalf("ReplaceVerifier(owner) error = %v", err)
	}
	if replaced.ID != first.ID || replaced.MemberID != first.MemberID ||
		!replaced.CreatedAt.Equal(first.CreatedAt) || !replaced.UpdatedAt.Equal(updatedAt) ||
		!bytes.Equal(replaced.Verifier.Bytes(), newVerifier.Bytes()) {
		t.Fatalf("ReplaceVerifier(owner) = %+v", replaced)
	}

	if err := repository.TouchLastUsedAt(t.Context(), first.MemberID, first.ID, first.Verifier, updatedAt); !errors.Is(err, patdomain.ErrTokenNotFound) {
		t.Fatalf("TouchLastUsedAt(stale verifier) error = %v, want ErrTokenNotFound", err)
	}
	assertLastUsedAt(t, db, first.ID, nil)

	latestUse := updatedAt.Add(time.Hour)
	if err := repository.TouchLastUsedAt(t.Context(), first.MemberID, first.ID, newVerifier, latestUse); err != nil {
		t.Fatalf("TouchLastUsedAt(latest) error = %v", err)
	}
	if err := repository.TouchLastUsedAt(t.Context(), first.MemberID, first.ID, newVerifier, updatedAt); err != nil {
		t.Fatalf("TouchLastUsedAt(older) error = %v", err)
	}
	assertLastUsedAt(t, db, first.ID, &latestUse)

	if err := repository.Delete(t.Context(), "member-2", first.ID); !errors.Is(err, patdomain.ErrTokenNotFound) {
		t.Fatalf("Delete(other Member) error = %v, want ErrTokenNotFound", err)
	}
	if _, err := repository.FindByID(t.Context(), first.ID); err != nil {
		t.Fatalf("other Member deleted token: %v", err)
	}
	if err := repository.Delete(t.Context(), first.MemberID, first.ID); err != nil {
		t.Fatalf("Delete(owner) error = %v", err)
	}
	if _, err := repository.FindByID(t.Context(), first.ID); !errors.Is(err, patdomain.ErrTokenNotFound) {
		t.Fatalf("FindByID(deleted) error = %v, want ErrTokenNotFound", err)
	}
}

func TestRepositoryStoresOnlySelectorAndSHA256Verifier(t *testing.T) {
	t.Parallel()

	db := newPATRepositoryTestDB(t)
	repository := mustPATRepository(t, db)
	clock := &mutableClock{now: time.Date(2026, time.August, 23, 2, 3, 4, 0, time.UTC)}
	service, err := patdomain.NewService(repository, clock, bytes.NewReader(bytes.Repeat([]byte{0x42}, 48)))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	issued, err := service.Create(t.Context(), "member-1")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	var stored struct {
		Selector   string `gorm:"column:selector"`
		SecretHash []byte `gorm:"column:secret_hash"`
	}
	if err := db.Table(personalAccessTokenTable).Where("selector = ?", string(issued.Metadata.ID)).Take(&stored).Error; err != nil {
		t.Fatalf("load raw persisted credential: %v", err)
	}
	if stored.Selector != string(issued.Metadata.ID) || len(stored.SecretHash) != 32 {
		t.Fatalf("persisted credential = selector:%q hashLength:%d", stored.Selector, len(stored.SecretHash))
	}
	if strings.Contains(string(stored.SecretHash), issued.Secret.Reveal()) || strings.Contains(fmt.Sprintf("%x", stored.SecretHash), issued.Secret.Reveal()) {
		t.Fatal("persisted verifier contains bearer secret")
	}
}

func TestSuccessfulAuthenticationAloneTouchesLastUsedAtAndFailsClosedOnCredentialRace(t *testing.T) {
	t.Parallel()

	db := newPATRepositoryTestDB(t)
	repository := mustPATRepository(t, db)
	clock := &mutableClock{now: time.Date(2026, time.August, 23, 3, 4, 5, 0, time.UTC)}
	random := bytes.NewReader(bytes.Repeat([]byte{0x31}, 16+32+32))
	service, err := patdomain.NewService(repository, clock, random)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	issued, err := service.Create(t.Context(), "member-1")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	wrongSecret := issued.Secret.Reveal()[:len(issued.Secret.Reveal())-1] + "A"
	if strings.HasSuffix(issued.Secret.Reveal(), "A") {
		wrongSecret = issued.Secret.Reveal()[:len(issued.Secret.Reveal())-1] + "B"
	}
	if _, err := service.Authenticate(t.Context(), wrongSecret); !errors.Is(err, patdomain.ErrInvalidToken) {
		t.Fatalf("Authenticate(wrong secret) error = %v", err)
	}
	assertLastUsedAt(t, db, issued.Metadata.ID, nil)

	clock.now = clock.now.Add(time.Minute)
	if _, err := service.Authenticate(t.Context(), issued.Secret.Reveal()); err != nil {
		t.Fatalf("Authenticate(valid) error = %v", err)
	}
	assertLastUsedAt(t, db, issued.Metadata.ID, &clock.now)

	loaded, err := repository.FindByID(t.Context(), issued.Metadata.ID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	replacement := verifier(t, 0x77)
	if _, err := repository.ReplaceVerifier(t.Context(), loaded.MemberID, loaded.ID, replacement, clock.now.Add(time.Minute)); err != nil {
		t.Fatalf("ReplaceVerifier() error = %v", err)
	}
	if err := repository.TouchLastUsedAt(t.Context(), loaded.MemberID, loaded.ID, loaded.Verifier, clock.now.Add(2*time.Minute)); !errors.Is(err, patdomain.ErrTokenNotFound) {
		t.Fatalf("TouchLastUsedAt(pre-regeneration verifier) error = %v, want ErrTokenNotFound", err)
	}
}

func TestRepositoryFailsClosedOnInvalidPersistedVerifier(t *testing.T) {
	t.Parallel()

	db := newPATRepositoryTestDB(t)
	repository := mustPATRepository(t, db)
	now := time.Date(2026, time.August, 23, 4, 5, 6, 0, time.UTC)
	for _, test := range []struct {
		name       string
		selector   string
		secretHash []byte
	}{
		{name: "wrong verifier length", selector: tokenID(5), secretHash: bytes.Repeat([]byte{1}, 31)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := db.Exec(
				`INSERT INTO member_personal_access_token
					(selector, member_id, secret_hash, created_at, updated_at)
                 VALUES (?, ?, ?, ?, ?)`,
				test.selector,
				"member-1",
				test.secretHash,
				now,
				now,
			).Error; err != nil {
				t.Fatalf("insert malformed row: %v", err)
			}
			if _, err := repository.FindByID(t.Context(), patdomain.TokenID(test.selector)); !errors.Is(err, patdomain.ErrInvalidStoredToken) {
				t.Fatalf("FindByID() error = %v, want ErrInvalidStoredToken", err)
			}
		})
	}
}

func TestNewPersonalAccessTokenRepositoryRejectsNilDatabase(t *testing.T) {
	t.Parallel()

	repository, err := NewPersonalAccessTokenRepository(nil)
	if repository != nil || !errors.Is(err, patdomain.ErrInvalidDependencies) {
		t.Fatalf("NewPersonalAccessTokenRepository(nil) = %+v, %v", repository, err)
	}
}

func TestAuthenticationRequiresCurrentActiveMember(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change string
	}{
		{"inactive identity", `UPDATE kratos.identities SET state = 'inactive' WHERE id = 'identity-1'`},
		{"banned identity", `UPDATE kratos.identities SET metadata_admin = '{"banned":true}' WHERE id = 'identity-1'`},
		{"deleted Member", `UPDATE member SET deleted_at = CURRENT_TIMESTAMP WHERE id = 'member-1'`},
		{"missing Member", `DELETE FROM member WHERE id = 'member-1'`},
		{"missing identity", `DELETE FROM kratos.identities WHERE id = 'identity-1'`},
		{"unlinked identity", `UPDATE member SET account_identity_id = NULL WHERE id = 'member-1'`},
		{"different identity link", `UPDATE member SET account_identity_id = 'identity-2' WHERE id = 'member-1'`},
		{"mismatched external id", `UPDATE kratos.identities SET external_id = 'member-2' WHERE id = 'identity-1'`},
		{"incomplete onboarding", `UPDATE member SET onboarded = FALSE WHERE id = 'member-1'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newPATRepositoryTestDB(t)
			repository := mustPATRepository(t, db)
			clock := &mutableClock{now: time.Now().UTC()}
			service, err := patdomain.NewService(repository, clock, bytes.NewReader(bytes.Repeat([]byte{0x42}, 48)))
			if err != nil {
				t.Fatal(err)
			}
			issued, err := service.Create(t.Context(), "member-1")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Authenticate(t.Context(), issued.Secret.Reveal()); err != nil {
				t.Fatalf("active owner rejected: %v", err)
			}
			if err := db.Exec(tc.change).Error; err != nil {
				t.Fatal(err)
			}
			clock.now = clock.now.Add(time.Minute)
			if _, err := service.Authenticate(t.Context(), issued.Secret.Reveal()); !errors.Is(err, patdomain.ErrInvalidToken) {
				t.Fatalf("inactive owner authentication error %v", err)
			}
			originalUse := clock.now.Add(-time.Minute)
			assertLastUsedAt(t, db, issued.Metadata.ID, &originalUse)
		})
	}
}

func TestOathkeeperUsesCurrentPATServiceAfterRegenerationAndDeletion(t *testing.T) {
	t.Parallel()
	db := newPATRepositoryTestDB(t)
	repository := mustPATRepository(t, db)
	clock := &mutableClock{now: time.Now().UTC()}
	service, err := patdomain.NewService(repository, clock, bytes.NewReader(append(bytes.Repeat([]byte{1}, 48), bytes.Repeat([]byte{2}, 32)...)))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Create(t.Context(), "member-1")
	if err != nil {
		t.Fatal(err)
	}
	const secret = "internal-service-secret"
	handler, err := patauth.NewOathkeeper(secret, "X-Internal-Service", service)
	if err != nil {
		t.Fatal(err)
	}
	check := func(token string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, patauth.OathkeeperPath, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Internal-Service", secret)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("status %d want %d", w.Code, want)
		}
	}
	check(issued.Secret.Reveal(), 200)
	clock.now = clock.now.Add(time.Minute)
	regenerated, err := service.Regenerate(t.Context(), "member-1", issued.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	check(issued.Secret.Reveal(), 401)
	check(regenerated.Secret.Reveal(), 200)
	if err := service.Delete(t.Context(), "member-1", issued.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	check(regenerated.Secret.Reveal(), 401)
}

type mutableClock struct{ now time.Time }

func (clock *mutableClock) Now() time.Time { return clock.now }

func newPATRepositoryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := sqlitetest.Open(t, sqlite.Open, &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, statement := range []string{
		`ATTACH DATABASE ':memory:' AS kratos`,
		`CREATE TABLE kratos.identities (id TEXT PRIMARY KEY, external_id TEXT, state TEXT, metadata_admin TEXT)`,
		`CREATE TABLE member (id TEXT PRIMARY KEY, account_identity_id TEXT, deleted_at DATETIME, onboarded BOOLEAN)`,
		`INSERT INTO kratos.identities VALUES ('identity-1', 'member-1', 'active', '{}'), ('identity-2', 'member-2', 'active', '{}')`,
		`INSERT INTO member VALUES ('member-1', 'identity-1', NULL, TRUE), ('member-2', 'identity-2', NULL, TRUE)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("create active Member fixture: %v", err)
		}
	}
	if err := db.Exec(`CREATE TABLE member_personal_access_token (
        selector TEXT PRIMARY KEY,
        member_id TEXT NOT NULL UNIQUE,
        secret_hash BLOB NOT NULL,
        created_at DATETIME NOT NULL,
        updated_at DATETIME NOT NULL,
        last_used_at DATETIME NULL
    )`).Error; err != nil {
		t.Fatalf("create PAT repository table: %v", err)
	}
	return db
}

func mustPATRepository(t *testing.T, db *gorm.DB) *PersonalAccessTokenRepository {
	t.Helper()
	repository, err := NewPersonalAccessTokenRepository(db)
	if err != nil {
		t.Fatalf("NewPersonalAccessTokenRepository() error = %v", err)
	}
	return repository
}

func storedToken(
	t *testing.T,
	seed byte,
	memberID patdomain.MemberID,
	createdAt time.Time,
) patdomain.StoredToken {
	t.Helper()
	return patdomain.StoredToken{
		ID:        patdomain.TokenID(tokenID(seed)),
		MemberID:  memberID,
		Verifier:  verifier(t, seed),
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
}

func tokenID(seed byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{seed}, 16))
}

func verifier(t *testing.T, seed byte) patdomain.Verifier {
	t.Helper()
	value, err := patdomain.VerifierFromBytes(bytes.Repeat([]byte{seed}, 32))
	if err != nil {
		t.Fatalf("VerifierFromBytes() error = %v", err)
	}
	return value
}

func storedTokenEqual(left, right patdomain.StoredToken) bool {
	return left.ID == right.ID && left.MemberID == right.MemberID &&
		bytes.Equal(left.Verifier.Bytes(), right.Verifier.Bytes()) &&
		left.CreatedAt.Equal(right.CreatedAt) && left.UpdatedAt.Equal(right.UpdatedAt)
}

func assertLastUsedAt(t *testing.T, db *gorm.DB, tokenID patdomain.TokenID, want *time.Time) {
	t.Helper()
	var row struct {
		LastUsedAt *time.Time `gorm:"column:last_used_at"`
	}
	if err := db.Table(personalAccessTokenTable).Select("last_used_at").Where("selector = ?", string(tokenID)).Take(&row).Error; err != nil {
		t.Fatalf("load last_used_at: %v", err)
	}
	if want == nil {
		if row.LastUsedAt != nil {
			t.Fatalf("last_used_at = %v, want NULL", row.LastUsedAt)
		}
		return
	}
	if row.LastUsedAt == nil || !row.LastUsedAt.Equal(want.UTC()) {
		t.Fatalf("last_used_at = %v, want %v", row.LastUsedAt, want.UTC())
	}
}
