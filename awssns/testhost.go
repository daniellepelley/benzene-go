package awssns

import (
	"context"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"
)

// TestMessageID is the message ID SendSNS stamps on the single notification it delivers.
const TestMessageID = "benzenetest-sns-1"

// SendSNS pushes an SNS-to-Lambda notification for topic+payload (with optional headers) through
// the real awssns binding on host, and returns the native outcome: SNS has no partial-failure
// response, so a failed notification surfaces as a non-nil error (the async-invoke-retry signal
// awssns.Handler returns), and a handled one as nil. This is the SNS specialization+dispatch in
// one call - the Go counterpart of the reference AwsEventBuilder.CreateSnsEvent + SendEventAsync,
// parallel in name and argument order with SendSQS.
func SendSNS(t benzenetest.TB, host *benzenetest.Host, topic benzene.Topic, payload any, headers map[string]string) error {
	t.Helper()
	event := benzenetest.NewSNSEvent(t, TestMessageID, topic, payload, headers)
	_, err := Handler(host.Builder())(context.Background(), event)
	return err
}
