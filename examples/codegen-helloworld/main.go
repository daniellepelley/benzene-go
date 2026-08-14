package main

// main demonstrates a generated benzene-codegen client end to end: it wires
// paymentscapture.PaymentsCaptureClient (generated from contracts/payments.spec.json, see doc.go)
// to a real httpclient.Client and calls CapturePayments against a running Benzene service - e.g.
// benzene-dotnet's own AwsMesh Orders example, or any other conformant service that registers a
// payments:capture handler at the given envelope endpoint. main_test.go is the actual dogfood
// proof (a fake client.Sender, no live service needed); this command is a runnable illustration of
// how an application wires the generated client, matching this repo's other examples/*-helloworld
// commands.

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/daniellepelley/benzene-go/examples/codegen-helloworld/paymentscapture"
	"github.com/daniellepelley/benzene-go/httpclient"
)

func main() {
	endpoint := flag.String("endpoint", envOr("BENZENE_ENVELOPE_ENDPOINT", "http://localhost:8080/benzene/invoke"), "target service's envelope endpoint")
	orderID := flag.String("order-id", "order-123", "order id to capture payment for")
	amount := flag.Float64("amount", 42.42, "amount to capture")
	currency := flag.String("currency", "GBP", "currency code")
	flag.Parse()

	sender := httpclient.NewClient(*endpoint)
	c := paymentscapture.NewPaymentsCaptureClient(sender)

	result, err := c.CapturePayments(context.Background(), paymentscapture.CapturePayment{
		OrderId:  *orderID,
		Amount:   amount,
		Currency: *currency,
	})
	if err != nil {
		log.Fatalf("capture payment: %v", err)
	}

	if !result.IsSuccessful() {
		fmt.Printf("payments:capture failed: status=%s errors=%v\n", result.Status, result.Errors)
		os.Exit(1)
	}

	fmt.Printf("payments:capture ok: %+v (contractHash=%s)\n", result.Payload, paymentscapture.ContractHash)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
