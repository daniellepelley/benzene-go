package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/daniellepelley/benzene-go/awssns"
)

func invoke(t *testing.T, event string) error {
	t.Helper()
	handler := awssns.Handler(newApp())
	_, err := handler(context.Background(), json.RawMessage(event))
	return err
}

func TestConsumer_ValidGreetNotificationSucceeds(t *testing.T) {
	event := `{"Records":[{"Sns":{"MessageId":"msg-1","Message":"{\"name\":\"World\"}","MessageAttributes":{"topic":{"Value":"greet"}}}}]}`

	if err := invoke(t, event); err != nil {
		t.Errorf("invoke() error = %v, want nil", err)
	}
}

func TestConsumer_MissingNameIsReturnedAsError(t *testing.T) {
	event := `{"Records":[{"Sns":{"MessageId":"msg-1","Message":"{\"name\":\"\"}","MessageAttributes":{"topic":{"Value":"greet"}}}}]}`

	if err := invoke(t, event); err == nil {
		t.Error("invoke() error = nil, want an error for a failed notification - triggers AWS's async-invoke retry")
	}
}
