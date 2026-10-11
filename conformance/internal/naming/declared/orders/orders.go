// Package orders is naming/orders with the collision resolved: Item declares its schema name.
package orders

// Item is the fixture's orders.Item, declared as OrderItem.
type Item struct {
	SKU string `json:"sku"`
}

// BenzeneSchemaName declares Item's component name (contract.SchemaNamer).
func (Item) BenzeneSchemaName() string { return "OrderItem" }
