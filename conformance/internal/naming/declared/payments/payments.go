// Package payments is naming/payments with the collision resolved: Item declares its schema name.
package payments

// Item is the fixture's payments.Item, declared as PaymentItem.
type Item struct {
	PaymentID string `json:"paymentId"`
}

// BenzeneSchemaName declares Item's component name (contract.SchemaNamer).
func (Item) BenzeneSchemaName() string { return "PaymentItem" }
