package service

import "sync/atomic"

var (
	paymentWebhookFailures atomic.Int64
)

func RecordPaymentWebhookFailure() {
	paymentWebhookFailures.Add(1)
}

func FusionRuntimeFailureStats() (paymentFailures int64) {
	return paymentWebhookFailures.Load()
}
