package mq

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecideQueueDelivery(t *testing.T) {
	handlerErr := errors.New("handler failed")
	terminalErr := NewTerminalDeliveryError("email_invalid_recipient", handlerErr)

	tests := []struct {
		name         string
		shuttingDown bool
		err          error
		retryCount   int
		maxRetries   int
		wantAction   queueDeliveryAction
	}{
		{name: "success completes", wantAction: queueDeliveryComplete},
		{name: "handler error retries", err: handlerErr, retryCount: 1, maxRetries: 3, wantAction: queueDeliveryRetry},
		{name: "terminal error dead letters before retry exhaustion", err: terminalErr, retryCount: 0, maxRetries: 3, wantAction: queueDeliveryTerminal},
		{name: "retry exhaustion dead letters", err: handlerErr, retryCount: 3, maxRetries: 3, wantAction: queueDeliveryRetriesExhausted},
		{name: "retry count beyond limit stays exhausted", err: handlerErr, retryCount: 4, maxRetries: 3, wantAction: queueDeliveryRetriesExhausted},
		{name: "zero retries exhausts the first failure", err: handlerErr, retryCount: 0, maxRetries: 0, wantAction: queueDeliveryRetriesExhausted},
		{name: "shutdown takes priority over success", shuttingDown: true, wantAction: queueDeliveryShutdownRetry},
		{name: "shutdown takes priority over retry", shuttingDown: true, err: handlerErr, retryCount: 0, maxRetries: 3, wantAction: queueDeliveryShutdownRetry},
		{name: "shutdown takes priority over exhausted retry count", shuttingDown: true, err: handlerErr, retryCount: 4, maxRetries: 3, wantAction: queueDeliveryShutdownRetry},
		{name: "shutdown takes priority over terminal error", shuttingDown: true, err: terminalErr, retryCount: 0, maxRetries: 3, wantAction: queueDeliveryShutdownRetry},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := decideQueueDelivery(tt.shuttingDown, tt.err, tt.retryCount, tt.maxRetries)
			require.Equal(t, tt.wantAction, decision)
		})
	}
}
