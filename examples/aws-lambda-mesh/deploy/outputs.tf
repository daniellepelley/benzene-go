output "orders_url" {
  description = "POST an order here to kick off the cascade, e.g. curl -X POST <this>/orders -d '{\"customerId\":\"cust-1\",\"sku\":\"espresso\",\"quantity\":2}'."
  value       = "${aws_apigatewayv2_api.service["orders"].api_endpoint}/orders"
}

output "service_api_endpoints" {
  description = "Each service's HTTP API root."
  value       = { for k, api in aws_apigatewayv2_api.service : k => api.api_endpoint }
}

output "mesh_ui_url" {
  description = "Open this in a browser to see the Mesh View (services, health, topic catalog, recent flows). Populated as services announce/heartbeat/trace to the mesh Lambda."
  value       = aws_apigatewayv2_api.mesh.api_endpoint
}

output "mesh_function_name" {
  description = "The mesh Lambda. Invoke it directly to exercise its envelope endpoint without going through API Gateway: aws lambda invoke --function-name <this> --payload '...' /dev/stdout."
  value       = aws_lambda_function.mesh.function_name
}

output "discovered_url" {
  description = "GET this for {\"discovered\":N} - a one-curl assertion of how many services have registered with the mesh so far (see cmd/mesh/main.go)."
  value       = "${aws_apigatewayv2_api.mesh.api_endpoint}/mesh/discovered"
}
