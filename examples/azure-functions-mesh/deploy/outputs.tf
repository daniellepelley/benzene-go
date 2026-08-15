output "orders_url" {
  description = "POST an order here to kick off the cascade, e.g. curl -X POST <this> -d '{\"customerId\":\"cust-1\",\"sku\":\"espresso\",\"quantity\":2}'."
  value       = "https://${azurerm_linux_function_app.service["orders"].default_hostname}/orders"
}

output "service_hostnames" {
  description = "Each service Function App's default hostname."
  value       = { for k, app in azurerm_linux_function_app.service : k => app.default_hostname }
}

output "mesh_ui_url" {
  description = "Open this in a browser to see the Fleet View (services, health, topic catalog, recent flows). Populated as services announce/heartbeat/trace to the mesh Function."
  value       = "https://${azurerm_linux_function_app.mesh.default_hostname}/benzene/fleet-ui"
}

output "mesh_hostname" {
  description = "The mesh Function App's default hostname."
  value       = azurerm_linux_function_app.mesh.default_hostname
}

output "discovered_url" {
  description = "GET this for {\"discovered\":N} - a one-curl assertion of how many services have registered with the mesh so far (see cmd/mesh/main.go)."
  value       = "https://${azurerm_linux_function_app.mesh.default_hostname}/mesh/discovered"
}
