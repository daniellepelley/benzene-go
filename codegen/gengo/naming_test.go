package gengo

import "testing"

func TestTopicReversedMethodName(t *testing.T) {
	cases := map[string]string{
		"payments:capture": "CapturePayments",
		"payments:get-all": "GetallPayments",
		"orders:create":    "CreateOrders",
		"benzene:spec":     "SpecBenzene",
		"greet":            "Greet",
		"a:b:c":            "CBA",
		"1foo:2bar":        "_2bar_1foo",
	}
	for topic, want := range cases {
		if got := TopicReversedMethodName(topic); got != want {
			t.Errorf("TopicReversedMethodName(%q) = %q, want %q", topic, got, want)
		}
	}
}

func TestTopicMethodName(t *testing.T) {
	cases := map[string]string{
		"payments:capture": "PaymentsCapture",
		"payments:get-all": "PaymentsGetall",
		"orders:create":    "OrdersCreate",
		"benzene:spec":     "BenzeneSpec",
		"greet":            "Greet",
	}
	for topic, want := range cases {
		if got := TopicMethodName(topic); got != want {
			t.Errorf("TopicMethodName(%q) = %q, want %q", topic, got, want)
		}
	}
}

func TestFormatGoName(t *testing.T) {
	cases := map[string]string{
		"customerId":  "CustomerId",
		"CreateOrder": "CreateOrder",
		"order id":    "Orderid",
		"1Field":      "_1Field",
	}
	for in, want := range cases {
		if got := FormatGoName(in); got != want {
			t.Errorf("FormatGoName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateGoIdentifier(t *testing.T) {
	valid := []string{"payments", "orders_v2", "_private", "a1"}
	for _, v := range valid {
		if err := ValidateGoIdentifier(v); err != nil {
			t.Errorf("ValidateGoIdentifier(%q) = %v, want nil", v, err)
		}
	}

	invalid := []string{"", "1payments", "has space", "has-dash", "func", "orders.v2"}
	for _, v := range invalid {
		if err := ValidateGoIdentifier(v); err == nil {
			t.Errorf("ValidateGoIdentifier(%q) = nil, want error", v)
		}
	}
}
