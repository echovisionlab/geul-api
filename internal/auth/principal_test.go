package auth

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const (
	testIdentityID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	testMemberID   = "bbbbbbbb-bbbb-4bbb-9bbb-bbbbbbbbbbbb"
	testSessionID  = "cccccccc-cccc-4ccc-accc-cccccccccccc"
)

const principalTestDriverName = "geul-auth-principal-test"

var (
	registerPrincipalTestDriver sync.Once
	principalTestScenarios      sync.Map
)

type principalTestScenario struct {
	mu         sync.Mutex
	row        []driver.Value
	queryErr   error
	query      string
	args       []driver.NamedValue
	queryCount int
}

func (s *principalTestScenario) snapshot() (string, []driver.NamedValue, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.query, append([]driver.NamedValue(nil), s.args...), s.queryCount
}

type principalTestDriver struct{}

func (principalTestDriver) Open(name string) (driver.Conn, error) {
	value, ok := principalTestScenarios.Load(name)
	if !ok {
		return nil, errors.New("unknown auth principal test database")
	}
	return &principalTestConn{scenario: value.(*principalTestScenario)}, nil
}

type principalTestConn struct{ scenario *principalTestScenario }

func (*principalTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not supported")
}
func (*principalTestConn) Close() error { return nil }
func (*principalTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (c *principalTestConn) QueryContext(
	_ context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Rows, error) {
	c.scenario.mu.Lock()
	defer c.scenario.mu.Unlock()
	c.scenario.query = query
	c.scenario.args = append([]driver.NamedValue(nil), args...)
	c.scenario.queryCount++
	if c.scenario.queryErr != nil {
		return nil, c.scenario.queryErr
	}
	return &principalTestRows{row: append([]driver.Value(nil), c.scenario.row...)}, nil
}

var _ driver.QueryerContext = (*principalTestConn)(nil)

type principalTestRows struct {
	row  []driver.Value
	done bool
}

func (*principalTestRows) Columns() []string {
	return []string{"session_id", "identity_id", "member_id", "authenticated_at", "banned", "onboarded"}
}
func (*principalTestRows) Close() error { return nil }
func (r *principalTestRows) Next(destination []driver.Value) error {
	if r.done || len(r.row) == 0 {
		return io.EOF
	}
	copy(destination, r.row)
	r.done = true
	return nil
}

func newPrincipalTestDB(t *testing.T, banned bool, identityState string) (*gorm.DB, *principalTestScenario) {
	return newPrincipalTestDBWithOnboarding(t, banned, identityState, true)
}

func newPrincipalTestDBWithOnboarding(t *testing.T, banned bool, identityState string, onboarded bool) (*gorm.DB, *principalTestScenario) {
	t.Helper()
	registerPrincipalTestDriver.Do(func() {
		sql.Register(principalTestDriverName, principalTestDriver{})
	})
	scenario := &principalTestScenario{}
	if identityState == KratosStateActive {
		scenario.row = []driver.Value{testSessionID, testIdentityID, testMemberID, time.Now().UTC(), banned, onboarded}
	}
	dsn := uuid.NewString()
	principalTestScenarios.Store(dsn, scenario)
	sqlDB, err := sql.Open(principalTestDriverName, dsn)
	if err != nil {
		t.Fatalf("open principal test database: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
		principalTestScenarios.Delete(dsn)
	})
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableAutomaticPing: true,
	})
	if err != nil {
		t.Fatalf("open GORM principal test database: %v", err)
	}
	return db, scenario
}
