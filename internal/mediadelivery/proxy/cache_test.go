package proxy

import (
	"context"
	"errors"
	"testing"
)

type fakeCacheStore struct {
	data        []byte
	contentType string
	getErr      error
	putErr      error
	putCalls    int
}

func (s *fakeCacheStore) Get(context.Context, string) ([]byte, string, error) {
	return s.data, s.contentType, s.getErr
}

func (s *fakeCacheStore) Put(_ context.Context, _ string, data []byte, contentType string) error {
	s.putCalls++
	s.data = append([]byte(nil), data...)
	s.contentType = contentType
	return s.putErr
}

func runSynchronously(task func()) {
	task()
}

func TestRunInBackground(t *testing.T) {
	done := make(chan struct{})
	runInBackground(func() { close(done) })
	<-done
}

func TestFakeCacheStore(t *testing.T) {
	want := errors.New("get failed")
	store := &fakeCacheStore{getErr: want}
	if _, _, err := store.Get(t.Context(), "key"); !errors.Is(err, want) {
		t.Fatalf("Get() error = %v", err)
	}
	store.getErr = nil
	if err := store.Put(t.Context(), "key", []byte("data"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if string(store.data) != "data" || store.contentType != "text/plain" || store.putCalls != 1 {
		t.Fatalf("store = %#v", store)
	}
}
