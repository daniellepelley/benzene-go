// Package payments holds the other half of contract-document-cases.json's namingCases: an Item of a
// different shape from orders.Item.
package payments

// Item is the fixture's payments.Item.
type Item struct {
	PaymentID string `json:"paymentId"`
}
