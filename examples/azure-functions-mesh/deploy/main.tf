# Azure deployment for examples/azure-functions-mesh: six chained Cloud Service Azure Functions
# (orders/payments/shipping/inventory/notifications/analytics) talking over Service Bus/Event
# Hub/Event Grid, plus a seventh "mesh" Function that ingests their register/heartbeat/trace/issue
# reports and serves the fleet UI - the Go counterpart of benzene-dotnet's
# examples/AzureFunctionsMesh/deploy, modeled closely on its real Azure resource shape (storage
# account, Consumption plan, seven Function Apps, Service Bus namespace+queues, Event Hub
# namespace+hub, Event Grid topic+subscriptions) and on this repo's own
# examples/aws-lambda-mesh/deploy Terraform style, with the changes ../README.md's "Divergence
# from .NET" section explains in full: this port's mesh is PUSH-based (services invoke the mesh
# Function's own /benzene/invoke directly over plain HTTP; the mesh never lists, tags-queries, or
# calls the services), so there is NO Azure Resource Manager discovery role, NO managed identity,
# and NO Blob catalog container/storage container anywhere below - a materially simpler IAM story
# than .NET's pull-based stack. Application Insights/usage tracking is likewise out of scope for
# this port (see the README).
#
# Remote state in Azure Blob (configured at init via -backend-config, matching .NET's own
# azurerm backend convention and this repo's other Azure deploy workflows).

terraform {
  required_version = ">= 1.5.0"
  backend "azurerm" {}
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}

provider "azurerm" {
  features {}
  # The workflow registers the resource providers this stack needs (Storage, Web, EventHub,
  # ServiceBus, EventGrid) before Terraform runs, so the provider need not mass-register on every
  # apply.
  resource_provider_registrations = "none"
}

locals {
  # The six Cloud Service Functions (tagged for discovery-parity - see var.discovery_tag_key;
  # nothing in this port actually reads the tag, kept only as documentation, the same stance
  # examples/aws-lambda-mesh/deploy/variables.tf's own discovery_tag_key takes). orders/payments/
  # shipping form the command chain + publish events; inventory/notifications/analytics are pure
  # event consumers.
  services = ["orders", "payments", "shipping", "inventory", "notifications", "analytics"]

  mesh_invoke_url = "https://${azurerm_linux_function_app.mesh.default_hostname}/benzene/invoke"

  # Per-service app settings - the messaging connection strings + entity names each service
  # actually uses, merged with the common settings on each Function App below. Every service also
  # gets MESH_INVOKE_URL so it can push register/heartbeat/trace/issue reports to the mesh
  # Function over plain HTTP (meshapp.App.RunHeartbeatLoop/HTTPHandler - see ../meshapp/meshapp.go).
  service_app_settings = {
    orders = {
      ServiceBusConnection = azurerm_servicebus_namespace.this.default_primary_connection_string
      PAYMENTS_QUEUE       = azurerm_servicebus_queue.payments.name
      EventHubConnection   = azurerm_eventhub_namespace.this.default_primary_connection_string
      ORDER_PLACED_HUB     = azurerm_eventhub.order_placed.name
      MESH_INVOKE_URL      = local.mesh_invoke_url
    }
    payments = {
      ServiceBusConnection = azurerm_servicebus_namespace.this.default_primary_connection_string
      PAYMENTS_QUEUE       = azurerm_servicebus_queue.payments.name
      SHIPPING_QUEUE       = azurerm_servicebus_queue.shipping.name
      EventGridEndpoint    = azurerm_eventgrid_topic.this.endpoint
      EventGridKey         = azurerm_eventgrid_topic.this.primary_access_key
      MESH_INVOKE_URL      = local.mesh_invoke_url
    }
    shipping = {
      ServiceBusConnection = azurerm_servicebus_namespace.this.default_primary_connection_string
      SHIPPING_QUEUE       = azurerm_servicebus_queue.shipping.name
      EventGridEndpoint    = azurerm_eventgrid_topic.this.endpoint
      EventGridKey         = azurerm_eventgrid_topic.this.primary_access_key
      MESH_INVOKE_URL      = local.mesh_invoke_url
    }
    inventory = {
      EventHubConnection = azurerm_eventhub_namespace.this.default_primary_connection_string
      ORDER_PLACED_HUB   = azurerm_eventhub.order_placed.name
      MESH_INVOKE_URL    = local.mesh_invoke_url
    }
    notifications = {
      EventHubConnection = azurerm_eventhub_namespace.this.default_primary_connection_string
      ORDER_PLACED_HUB   = azurerm_eventhub.order_placed.name
      MESH_INVOKE_URL    = local.mesh_invoke_url
    }
    analytics = {
      MESH_INVOKE_URL = local.mesh_invoke_url
    }
  }

  # Event Grid routing: which consumer Function's EventGridTrigger function each event type fans
  # out to (matched by the event's own type = the Benzene topic). One subscription per consumer
  # service, filtered to the event type(s) that service's own domain.Register call actually
  # registers (see ../README.md's topology table) - inventory only wants shipment:dispatched;
  # notifications and analytics want both.
  eventgrid_routes = {
    inventory     = { event_types = ["shipment:dispatched"], function = "ShipmentDispatched" }
    notifications = { event_types = ["payment:captured", "shipment:dispatched"], function = "IntegrationEvents" }
    analytics     = { event_types = ["payment:captured", "shipment:dispatched"], function = "IntegrationEvents" }
  }
}

# The resource group is bootstrapped imperatively by the workflow (`az group create`, idempotent)
# before Terraform runs - it holds the remote-state storage account too - so Terraform reads it
# rather than owning it, matching benzene-dotnet's examples/AzureFunctionsMesh/deploy/main.tf.
data "azurerm_resource_group" "this" {
  name = var.resource_group
}

# --- Storage: the Functions runtime store only - no mesh catalog container (push-based, no
# catalog store - see the file header). --------------------------------------------------------
resource "azurerm_storage_account" "this" {
  name                     = var.storage_account
  resource_group_name      = data.azurerm_resource_group.this.name
  location                 = data.azurerm_resource_group.this.location
  account_tier             = "Standard"
  account_replication_type = "LRS"
}

# --- Consumption plan (Linux) ------------------------------------------------------------------
resource "azurerm_service_plan" "this" {
  name                = "${var.project}-plan"
  resource_group_name = data.azurerm_resource_group.this.name
  location            = data.azurerm_resource_group.this.location
  os_type             = "Linux"
  sku_name            = "Y1"
}

# --- The six Cloud Service Function Apps (tagged for discovery-parity) -------------------------
resource "azurerm_linux_function_app" "service" {
  for_each            = toset(local.services)
  name                = "${var.project}-${each.value}"
  resource_group_name = data.azurerm_resource_group.this.name
  location            = data.azurerm_resource_group.this.location
  service_plan_id     = azurerm_service_plan.this.id

  storage_account_name       = azurerm_storage_account.this.name
  storage_account_access_key = azurerm_storage_account.this.primary_access_key

  tags = { (var.discovery_tag_key) = "true" }

  site_config {
    # The Functions host (and a human) probe the Cloud Service's own health endpoint.
    # azurerm requires the eviction window whenever a health_check_path is set.
    health_check_path                 = "/benzene/health"
    health_check_eviction_time_in_min = 5
    # No application_stack block: a custom handler doesn't declare a language runtime stack -
    # FUNCTIONS_WORKER_RUNTIME=custom below is what actually selects the custom-handler model.
  }

  app_settings = merge(
    { FUNCTIONS_WORKER_RUNTIME = "custom" },
    local.service_app_settings[each.value]
  )

  # The code is published out-of-band by the workflow's zip-deploy step, which (on Linux
  # Consumption, where run-from-package is mandatory) sets WEBSITE_RUN_FROM_PACKAGE to the
  # uploaded package's URL. That setting is deliberately NOT declared above, so ignore it here:
  # otherwise the second apply (the one that wires the Event Grid subscriptions after publish)
  # sees it as drift and strips it, which un-deploys the functions - the same first-deploy
  # iteration point benzene-dotnet's own examples/AzureFunctionsMesh/deploy/main.tf documents and
  # works around identically.
  lifecycle {
    ignore_changes = [app_settings["WEBSITE_RUN_FROM_PACKAGE"]]
  }
}

# ---------------------------------------------------------------------------------------------
# Inter-service messaging - each transport used for what it's good at:
#   - Service Bus queues (point-to-point commands): orders -> payments (payment:take), payments ->
#     shipping (shipment:book).
#   - Event Hub (fan-out stream): orders publishes order:placed -> inventory + notifications, each
#     reading their own consumer group.
#   - Event Grid (routed integration events on a CloudEvents-schema topic): payments publishes
#     payment:captured, shipping publishes shipment:dispatched -> routed by event type to
#     inventory/notifications/analytics.
# ---------------------------------------------------------------------------------------------
resource "azurerm_servicebus_namespace" "this" {
  # A Service Bus namespace name may not end with "-sb"/"-mgmt" (reserved), so this is "-bus".
  name                = "${var.project}-bus"
  resource_group_name = data.azurerm_resource_group.this.name
  location            = data.azurerm_resource_group.this.location
  sku                 = "Standard" # cheapest SKU that supports queues (Basic has no topics/sessions)
}

resource "azurerm_servicebus_queue" "payments" {
  name         = "payments"
  namespace_id = azurerm_servicebus_namespace.this.id
}

resource "azurerm_servicebus_queue" "shipping" {
  name         = "shipping"
  namespace_id = azurerm_servicebus_namespace.this.id
}

resource "azurerm_eventhub_namespace" "this" {
  name                = "${var.project}-eh"
  resource_group_name = data.azurerm_resource_group.this.name
  location            = data.azurerm_resource_group.this.location
  sku                 = "Standard" # Basic has no consumer groups beyond $Default, so no fan-out
  capacity            = 1
}

resource "azurerm_eventhub" "order_placed" {
  name              = "order-placed"
  namespace_id      = azurerm_eventhub_namespace.this.id
  partition_count   = 2
  message_retention = 1
}

# One consumer group per subscriber, so inventory and notifications each read the whole stream
# independently (fan-out) - matching cmd/inventory's and cmd/notifications's own OrderPlaced/
# function.json consumerGroup values.
resource "azurerm_eventhub_consumer_group" "inventory" {
  name                = "inventory"
  namespace_name      = azurerm_eventhub_namespace.this.name
  eventhub_name       = azurerm_eventhub.order_placed.name
  resource_group_name = data.azurerm_resource_group.this.name
}

resource "azurerm_eventhub_consumer_group" "notifications" {
  name                = "notifications"
  namespace_name      = azurerm_eventhub_namespace.this.name
  eventhub_name       = azurerm_eventhub.order_placed.name
  resource_group_name = data.azurerm_resource_group.this.name
}

resource "azurerm_eventgrid_topic" "this" {
  name                = "${var.project}-eg"
  resource_group_name = data.azurerm_resource_group.this.name
  location            = data.azurerm_resource_group.this.location
  # azureeventgrid.Client (this port's outbound sender) publishes CloudEvents 1.0
  # (messaging.NewCloudEvent + azeventgrid.Client.PublishCloudEvents), so the topic must accept
  # that schema - the default "EventGridSchema" would reject them.
  input_schema = "CloudEventSchemaV1_0"
}

# Subscribing to a Consumption-plan Function's Event Grid trigger via azure_function_endpoint
# validates through an ARM control-plane lookup of the function, which is unreliable until the
# function has been published and warmed at least once ("Destination endpoint not found ...
# should pre-exist") - the same failure mode benzene-dotnet's own
# examples/AzureFunctionsMesh/deploy/main.tf hit and worked around. This stack takes the identical
# fix: subscribe via the Functions Event Grid extension's own WEBHOOK
# (/runtime/webhooks/eventgrid?functionName=<fn>&code=<system-key>), validated against the *live*
# running function instead of an ARM lookup. var.wire_eventgrid_subscriptions gates this whole
# block off on the FIRST apply (before the code is published) - the deploy workflow runs a SECOND
# apply with it set to true, after publishing and warming every consumer app.
data "azurerm_function_app_host_keys" "consumer" {
  for_each            = var.wire_eventgrid_subscriptions ? toset(keys(local.eventgrid_routes)) : []
  name                = azurerm_linux_function_app.service[each.value].name
  resource_group_name = data.azurerm_resource_group.this.name
}

resource "azurerm_eventgrid_event_subscription" "route" {
  for_each              = var.wire_eventgrid_subscriptions ? local.eventgrid_routes : {}
  name                  = "${each.key}-events"
  scope                 = azurerm_eventgrid_topic.this.id
  included_event_types  = each.value.event_types
  event_delivery_schema = "CloudEventSchemaV1_0"

  webhook_endpoint {
    url = "https://${azurerm_linux_function_app.service[each.key].default_hostname}/runtime/webhooks/eventgrid?functionName=${each.value.function}&code=${data.azurerm_function_app_host_keys.consumer[each.key].event_grid_extension_config_key}"
  }
}

# --- The mesh Function App (NOT tagged - see the file header for why it needs no identity at all,
# unlike .NET's pull-based mesh) -----------------------------------------------------------------
resource "azurerm_linux_function_app" "mesh" {
  name                = "${var.project}-mesh"
  resource_group_name = data.azurerm_resource_group.this.name
  location            = data.azurerm_resource_group.this.location
  service_plan_id     = azurerm_service_plan.this.id

  storage_account_name       = azurerm_storage_account.this.name
  storage_account_access_key = azurerm_storage_account.this.primary_access_key

  site_config {
    # The Fleet View returns 200, so the platform can detect and recycle a wedged mesh instance.
    health_check_path                 = "/benzene/fleet-ui"
    health_check_eviction_time_in_min = 5
  }

  app_settings = {
    FUNCTIONS_WORKER_RUNTIME = "custom"
  }

  # Same as the service apps: the mesh code is zip-deployed out-of-band, setting
  # WEBSITE_RUN_FROM_PACKAGE. Ignore it so the post-publish apply doesn't strip the package and
  # blank the Fleet View.
  lifecycle {
    ignore_changes = [app_settings["WEBSITE_RUN_FROM_PACKAGE"]]
  }
}
