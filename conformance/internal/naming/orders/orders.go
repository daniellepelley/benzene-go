// Package orders holds one half of contract-document-cases.json's namingCases: a type whose simple
// name, Item, collides with payments.Item.
package orders

// Item is the fixture's orders.Item.
type Item struct {
	SKU string `json:"sku"`
}
