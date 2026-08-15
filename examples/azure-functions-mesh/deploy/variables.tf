variable "resource_group" {
  description = "The resource group this stack deploys into (bootstrapped by the workflow via `az group create` before Terraform runs - see main.tf's data source)."
  type        = string
  default     = "benzene-go-fnmesh-rg"
}

variable "project" {
  description = "Name prefix for every resource this stack creates."
  type        = string
  default     = "benzene-go-fnmesh"
}

variable "storage_account" {
  description = "Globally-unique storage account name for the Functions runtime store. Must differ from other examples' storage accounts in the same subscription."
  type        = string
  default     = "benzenegofnmesh"
}

variable "discovery_tag_key" {
  description = <<-EOT
    The resource tag key the six service Function Apps carry (the mesh Function App deliberately
    does not). Kept for cross-language parity with benzene-dotnet's examples/AzureFunctionsMesh,
    whose mesh discovers services by this tag via Azure Resource Manager - this port's mesh does
    no ARM-side discovery at all (see ../README.md's "Divergence from .NET" section), so nothing
    here reads the tag; it exists purely as documentation, the same stance
    examples/aws-lambda-mesh/deploy/variables.tf's own discovery_tag_key takes.
  EOT
  type        = string
  default     = "benzene"
}

variable "wire_eventgrid_subscriptions" {
  description = <<-EOT
    Whether to create the Event Grid subscriptions (main.tf's azurerm_eventgrid_event_subscription
    block). Their webhook validation needs the target Function already published and warm, so the
    deploy workflow runs Terraform TWICE: false (the default) on the first apply (creates
    everything else, including the six/mesh Function Apps with no code deployed yet), then true on
    a second apply after publishing + warming every consumer app. Leaving it false also keeps the
    destroy workflow's default plan free of the live azurerm_function_app_host_keys data source
    (which would fail against a stopped app) while still destroying any subscriptions already
    recorded in state.
  EOT
  type        = bool
  default     = false
}
