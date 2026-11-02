package mutation_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/stretchr/testify/require"
)

// This file is the store contract pattern of docs/architecture.md in
// miniature. A domain puts the Store port in its package, the fake and the
// contract cases in <domain>test, and the PostgreSQL store in
// <domain>/postgres; here all three are test code around a note.

// notes is a domain Store port: domain values in and out, domain errors.
type notes interface {
	Insert(ctx context.Context, name string) error
	Lock(ctx context.Context, name string) (revision int, err error)
	Bump(ctx context.Context, name string, expected int) error
}

var (
	errDuplicateNote   = errors.New("note already exists")
	errNoteNotFound    = errors.New("note not found")
	errRevisionChanged = errors.New("note revision changed")
)

// fakeNotes is the in-memory notes Store.
type fakeNotes struct {
	mu        sync.Mutex
	revisions map[string]int
}

func (f *fakeNotes) Savepoint() mutationtest.Checkpoint {
	f.mu.Lock()
	defer f.mu.Unlock()
	revisions := mutationtest.ClonePersisted(f.revisions)
	return mutationtest.Checkpoint{
		State: map[string]any{
			"revisions": revisions,
		},
		Restore: func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.revisions = revisions
		},
	}
}

func (f *fakeNotes) Insert(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.revisions[name]; ok {
		return errDuplicateNote
	}
	f.revisions[name] = 1
	return nil
}

func (f *fakeNotes) Lock(_ context.Context, name string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	revision, ok := f.revisions[name]
	if !ok {
		return 0, errNoteNotFound
	}
	return revision, nil
}

func (f *fakeNotes) Bump(_ context.Context, name string, expected int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revisions[name] != expected || expected == 0 {
		return errRevisionChanged
	}
	f.revisions[name]++
	return nil
}

// notesContract is the behavior every notes Store has.
var notesContract = map[string]func(t *testing.T, h mutationtest.Harness[notes]){
	"an inserted note is at revision one and a second insert is a duplicate": func(t *testing.T, h mutationtest.Harness[notes]) {
		require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store notes) error { return store.Insert(ctx, "a") }))
		err := mutationtest.Transact(t, h, func(ctx context.Context, store notes) error { return store.Insert(ctx, "a") })
		require.ErrorIs(t, err, errDuplicateNote)
		require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store notes) error {
			revision, err := store.Lock(ctx, "a")
			require.Equal(t, 1, revision)
			return err
		}))
	},
	"a missing note is not found": func(t *testing.T, h mutationtest.Harness[notes]) {
		err := mutationtest.Transact(t, h, func(ctx context.Context, store notes) error {
			_, err := store.Lock(ctx, "missing")
			return err
		})
		require.ErrorIs(t, err, errNoteNotFound)
	},
	"a bump applies only at the expected revision": func(t *testing.T, h mutationtest.Harness[notes]) {
		require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store notes) error {
			if err := store.Insert(ctx, "a"); err != nil {
				return err
			}
			return store.Bump(ctx, "a", 1)
		}))
		err := mutationtest.Transact(t, h, func(ctx context.Context, store notes) error { return store.Bump(ctx, "a", 1) })
		require.ErrorIs(t, err, errRevisionChanged)
	},
	"a failed transaction leaves nothing behind": func(t *testing.T, h mutationtest.Harness[notes]) {
		failed := errors.New("later step failed")
		err := mutationtest.Transact(t, h, func(ctx context.Context, store notes) error {
			if err := store.Insert(ctx, "a"); err != nil {
				return err
			}
			return failed
		})
		require.ErrorIs(t, err, failed)
		require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store notes) error { return store.Insert(ctx, "a") }))
	},
	"a locked note makes a second writer wait for the first commit": func(t *testing.T, h mutationtest.Harness[notes]) {
		require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store notes) error { return store.Insert(ctx, "a") }))
		var seen int
		err := mutationtest.RequireSecondWaits(t, h,
			func(ctx context.Context, store notes) error { _, err := store.Lock(ctx, "a"); return err },
			func(ctx context.Context, store notes) error { return store.Bump(ctx, "a", 1) },
			func(ctx context.Context, store notes) (err error) { seen, err = store.Lock(ctx, "a"); return err },
		)
		require.NoError(t, err)
		require.Equal(t, 2, seen, "the second writer reads the first writer's commit")
	},
}

func TestFakeNotesKeepTheNotesContract(t *testing.T) {
	mutationtest.RunContract(t, func(*testing.T) mutationtest.Harness[notes] {
		return mutationtest.FakeHarness[notes](&fakeNotes{revisions: map[string]int{}})
	}, notesContract)
}
