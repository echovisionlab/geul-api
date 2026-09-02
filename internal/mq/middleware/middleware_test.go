package middleware

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoveryConvertsPanicToError(t *testing.T) {
	handler := Recovery()(func(context.Context, mq.Message) error {
		panic("boom")
	})

	err := handler(context.Background(), mq.Message{Queue: "jobs"})
	require.Error(t, err)
	assert.Equal(t, "queue handler panic", err.Error())
}

func TestRecoveryPassesSuccessfulHandler(t *testing.T) {
	called := false
	handler := Recovery()(func(context.Context, mq.Message) error {
		called = true
		return nil
	})

	require.NoError(t, handler(context.Background(), mq.Message{Queue: "jobs"}))
	assert.True(t, called)
}
