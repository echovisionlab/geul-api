package mq

type queueDeliveryAction uint8

const (
	queueDeliveryComplete queueDeliveryAction = iota
	queueDeliveryRetry
	queueDeliveryTerminal
	queueDeliveryRetriesExhausted
	queueDeliveryShutdownRetry
)

// decideQueueDelivery owns the handler-result policy while the consumer keeps
// responsibility for PGMQ handoffs and their telemetry.
func decideQueueDelivery(
	shuttingDown bool,
	handlerErr error,
	retryCount int,
	maxRetries int,
) queueDeliveryAction {
	if shuttingDown {
		return queueDeliveryShutdownRetry
	}
	if handlerErr == nil {
		return queueDeliveryComplete
	}
	if _, terminal := TerminalDeliveryErrorClass(handlerErr); terminal {
		return queueDeliveryTerminal
	}
	if retryCount >= maxRetries {
		return queueDeliveryRetriesExhausted
	}
	return queueDeliveryRetry
}
