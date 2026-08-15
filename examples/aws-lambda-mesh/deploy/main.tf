# AWS Lambda deployment for examples/aws-lambda-mesh: six chained Cloud Service Lambdas
# (orders/payments/shipping/inventory/notifications/analytics) talking over SQS/SNS/EventBridge,
# plus a seventh "mesh" Lambda that ingests their register/heartbeat/trace/issue reports and
# serves the fleet UI - the Go counterpart of benzene-dotnet's examples/AwsMesh/deploy and
# benzene-typescript's examples/aws-lambda-mesh/deploy/main.tf, modeled closely on the latter's
# shape with the changes ../README.md's "Divergence from .NET" section explains in full: this
# port's mesh is PUSH-based (services invoke the mesh Lambda directly; the mesh never lists,
# tags-queries, or invokes the services), so there is no S3 catalog bucket, no discovery IAM
# policy, and the invoke permission below runs the OPPOSITE direction from .NET's/TypeScript's
# pull-based stack (the service role can invoke the mesh function; the mesh role cannot invoke
# anything at all - it has no outbound calls whatsoever).
#
# Remote state in S3 (see deploy-eks-mesh-helloworld.yml/deploy-k8s-mesh-helloworld.yml's own
# per-account state bucket convention, which .github/workflows/mesh-example-aws-lambda-deploy.yml
# mirrors for this stack).

terraform {
  required_version = ">= 1.5.0"
  backend "s3" {}
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.region
}

data "aws_caller_identity" "current" {}

locals {
  # The six Cloud Service Lambdas. orders/payments/shipping form the command chain and publish
  # events; inventory/notifications/analytics are pure event consumers.
  services = {
    orders        = { zip = var.orders_zip, name = "${var.project}-orders" }
    payments      = { zip = var.payments_zip, name = "${var.project}-payments" }
    shipping      = { zip = var.shipping_zip, name = "${var.project}-shipping" }
    inventory     = { zip = var.inventory_zip, name = "${var.project}-inventory" }
    notifications = { zip = var.notifications_zip, name = "${var.project}-notifications" }
    analytics     = { zip = var.analytics_zip, name = "${var.project}-analytics" }
  }

  # Per-service outbound targets, handed to each producer as env vars (a pure consumer only gets
  # MESH_FUNCTION_NAME). Every service - producer or consumer - gets MESH_FUNCTION_NAME so it can
  # push register/heartbeat/trace/issue reports to the mesh Lambda via a direct Lambda Invoke
  # (meshapp.App.Announce / Handler - see ../meshapp/meshapp.go). A stable set of keys per service
  # keeps each function's environment block shape unchanged across applies (avoids the AWS
  # provider's "block count changed 0->1" plan bug when a value like a queue URL is only known
  # after apply).
  service_env = {
    orders = {
      PAYMENTS_QUEUE_URL     = aws_sqs_queue.payments.url
      ORDER_PLACED_TOPIC_ARN = aws_sns_topic.order_placed.arn
      MESH_FUNCTION_NAME     = aws_lambda_function.mesh.function_name
    }
    payments = {
      SHIPPING_QUEUE_URL = aws_sqs_queue.shipping.url
      EVENT_BUS_NAME     = aws_cloudwatch_event_bus.bus.name
      MESH_FUNCTION_NAME = aws_lambda_function.mesh.function_name
    }
    shipping = {
      EVENT_BUS_NAME     = aws_cloudwatch_event_bus.bus.name
      MESH_FUNCTION_NAME = aws_lambda_function.mesh.function_name
    }
    inventory     = { MESH_FUNCTION_NAME = aws_lambda_function.mesh.function_name }
    notifications = { MESH_FUNCTION_NAME = aws_lambda_function.mesh.function_name }
    analytics     = { MESH_FUNCTION_NAME = aws_lambda_function.mesh.function_name }
  }

  # SNS fan-out: order:placed is delivered to each of these service Lambdas.
  sns_order_placed_subscribers = toset(["inventory", "notifications"])

  # EventBridge routing: one rule per integration event (matched on detail-type = the Benzene
  # topic, exactly what awseventbridge.Client.Send writes), fanned out to the listed consumer
  # Lambdas. Rule keys are slugs (no ':') for valid resource names.
  eventbridge_rules = {
    payment_captured    = { detail_type = "payment:captured", targets = ["notifications", "analytics"] }
    shipment_dispatched = { detail_type = "shipment:dispatched", targets = ["inventory", "notifications", "analytics"] }
  }

  # Flatten {rule -> [targets]} to individual (rule, service) pairs for the per-target resources.
  eventbridge_targets = merge([
    for rule_key, rule in local.eventbridge_rules : {
      for svc in rule.targets : "${rule_key}-${svc}" => { rule_key = rule_key, service = svc }
    }
  ]...)
}

# ---------------------------------------------------------------------------------------------------
# IAM: a shared execution+messaging+mesh-invoke role for the six service Lambdas, and a logs-only
# role for the mesh Lambda (it has no outbound calls at all - see the file header).
# ---------------------------------------------------------------------------------------------------
data "aws_iam_policy_document" "lambda_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "service" {
  name               = "${var.project}-service-role"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume.json
}

resource "aws_iam_role_policy_attachment" "service_logs" {
  role       = aws_iam_role.service.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

resource "aws_iam_role" "mesh" {
  name               = "${var.project}-mesh-role"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume.json
}

resource "aws_iam_role_policy_attachment" "mesh_logs" {
  role       = aws_iam_role.mesh.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

# The shared service role: send to both queues (and consume them - the event-source mapping polls
# with the function's role), publish the SNS topic, put events on the bus, and invoke the mesh
# function directly (the push - not pull - direction: see the file header).
data "aws_iam_policy_document" "service_messaging" {
  statement {
    actions   = ["sqs:SendMessage", "sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:GetQueueAttributes"]
    resources = [aws_sqs_queue.payments.arn, aws_sqs_queue.shipping.arn]
  }
  statement {
    actions   = ["sns:Publish"]
    resources = [aws_sns_topic.order_placed.arn]
  }
  statement {
    actions   = ["events:PutEvents"]
    resources = [aws_cloudwatch_event_bus.bus.arn]
  }
  statement {
    actions   = ["lambda:InvokeFunction"]
    resources = [aws_lambda_function.mesh.arn]
  }
}

resource "aws_iam_role_policy" "service_messaging" {
  name   = "${var.project}-service-messaging"
  role   = aws_iam_role.service.id
  policy = data.aws_iam_policy_document.service_messaging.json
}

# ---------------------------------------------------------------------------------------------------
# The six Cloud Service Lambdas (tagged for discovery-parity, see var.discovery_tag_key) + one
# HTTP API each.
# ---------------------------------------------------------------------------------------------------
resource "aws_lambda_function" "service" {
  for_each = local.services

  function_name    = each.value.name
  role             = aws_iam_role.service.arn
  filename         = each.value.zip
  source_code_hash = filebase64sha256(each.value.zip)
  runtime          = var.aws_lambda_managed_runtime
  handler          = "bootstrap"
  architectures    = [var.lambda_architecture]
  memory_size      = 256
  timeout          = 30

  environment {
    variables = local.service_env[each.key]
  }

  tags = { (var.discovery_tag_key) = "true" }
}

# ---------------------------------------------------------------------------------------------------
# Runtime interconnectivity - each transport used for what it's good at:
#   - SQS (point-to-point commands): orders -> payments (payments:capture), payments -> shipping
#     (shipping:book). Each queue triggers its service Lambda (event-source mapping).
#   - SNS (fan-out event): orders publishes order:placed -> inventory AND notifications
#     (subscriptions).
#   - EventBridge (routed integration events on a custom bus): payments publishes
#     payment:captured, shipping publishes shipment:dispatched -> routed by rule to
#     notifications/inventory/analytics.
# ---------------------------------------------------------------------------------------------------

# --- SQS: the point-to-point command hops -----------------------------------------------------------
resource "aws_sqs_queue" "payments" {
  name                       = "${var.project}-payments-queue"
  visibility_timeout_seconds = 60
}

resource "aws_sqs_queue" "shipping" {
  name                       = "${var.project}-shipping-queue"
  visibility_timeout_seconds = 60
}

resource "aws_lambda_event_source_mapping" "payments" {
  event_source_arn        = aws_sqs_queue.payments.arn
  function_name           = aws_lambda_function.service["payments"].arn
  batch_size              = 1
  function_response_types = ["ReportBatchItemFailures"]
}

resource "aws_lambda_event_source_mapping" "shipping" {
  event_source_arn        = aws_sqs_queue.shipping.arn
  function_name           = aws_lambda_function.service["shipping"].arn
  batch_size              = 1
  function_response_types = ["ReportBatchItemFailures"]
}

# --- SNS: the order:placed fan-out ------------------------------------------------------------------
resource "aws_sns_topic" "order_placed" {
  name = "${var.project}-order-placed"
}

resource "aws_sns_topic_subscription" "order_placed" {
  for_each  = local.sns_order_placed_subscribers
  topic_arn = aws_sns_topic.order_placed.arn
  protocol  = "lambda"
  endpoint  = aws_lambda_function.service[each.key].arn
}

resource "aws_lambda_permission" "sns_invoke" {
  for_each      = local.sns_order_placed_subscribers
  statement_id  = "AllowSnsInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.service[each.key].function_name
  principal     = "sns.amazonaws.com"
  source_arn    = aws_sns_topic.order_placed.arn
}

# --- EventBridge: the routed integration events on a dedicated bus -----------------------------------
resource "aws_cloudwatch_event_bus" "bus" {
  name = "${var.project}-bus"
}

resource "aws_cloudwatch_event_rule" "integration" {
  for_each       = local.eventbridge_rules
  name           = "${var.project}-${each.key}"
  event_bus_name = aws_cloudwatch_event_bus.bus.name
  event_pattern  = jsonencode({ "detail-type" = [each.value.detail_type] })
}

resource "aws_cloudwatch_event_target" "integration" {
  for_each       = local.eventbridge_targets
  rule           = aws_cloudwatch_event_rule.integration[each.value.rule_key].name
  event_bus_name = aws_cloudwatch_event_bus.bus.name
  target_id      = each.value.service
  arn            = aws_lambda_function.service[each.value.service].arn
}

resource "aws_lambda_permission" "eventbridge_invoke" {
  for_each      = local.eventbridge_targets
  statement_id  = "AllowEventBridge-${each.key}"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.service[each.value.service].function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.integration[each.value.rule_key].arn
}

# ---------------------------------------------------------------------------------------------------
# One HTTP API per service: a $default catch-all proxies the full path through. The meaningful
# route is orders' POST /orders (kicks off the cascade); the others expose each service's HTTP
# domain surface (currently just GET /benzene/health - see ../meshapp/meshapp.go).
# ---------------------------------------------------------------------------------------------------
resource "aws_apigatewayv2_api" "service" {
  for_each      = local.services
  name          = "${each.value.name}-api"
  protocol_type = "HTTP"
}

resource "aws_apigatewayv2_integration" "service" {
  for_each               = local.services
  api_id                 = aws_apigatewayv2_api.service[each.key].id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.service[each.key].invoke_arn
  payload_format_version = "2.0" # matches the requestContext.http shape meshapp.classify/awslambda.HTTPHandler parse
}

resource "aws_apigatewayv2_route" "service" {
  for_each  = local.services
  api_id    = aws_apigatewayv2_api.service[each.key].id
  route_key = "$default"
  target    = "integrations/${aws_apigatewayv2_integration.service[each.key].id}"
}

resource "aws_apigatewayv2_stage" "service" {
  for_each    = local.services
  api_id      = aws_apigatewayv2_api.service[each.key].id
  name        = "$default"
  auto_deploy = true
}

resource "aws_lambda_permission" "service_api" {
  for_each      = local.services
  statement_id  = "AllowApiGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.service[each.key].function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.service[each.key].execution_arn}/*/*"
}

# ---------------------------------------------------------------------------------------------------
# The mesh Lambda (NOT tagged for discovery-parity) + its own HTTP API. Unlike .NET's/TypeScript's
# pull-based mesh (an untriggered, schedule-fired aggregator with no HTTP surface at all), this
# port's mesh IS the live fleet UI - meshd.ViewHandler serves GET /benzene/fleet-ui directly (see
# ../cmd/mesh/main.go), so it needs an API Gateway front door like the six services, not a
# schedule. reserved_concurrent_executions is pinned (var.mesh_reserved_concurrency, default 1) so
# meshd.Collector's in-memory fleet state is a single, consistent instance - see
# variables.tf's doc comment and ../README.md's "Divergence from .NET" section for the full story.
# ---------------------------------------------------------------------------------------------------
resource "aws_lambda_function" "mesh" {
  function_name    = "${var.project}-mesh"
  role             = aws_iam_role.mesh.arn
  filename         = var.mesh_zip
  source_code_hash = filebase64sha256(var.mesh_zip)
  runtime          = var.aws_lambda_managed_runtime
  handler          = "bootstrap"
  architectures    = [var.lambda_architecture]
  memory_size      = 256
  timeout          = 30

  reserved_concurrent_executions = var.mesh_reserved_concurrency
}

resource "aws_apigatewayv2_api" "mesh" {
  name          = "${var.project}-mesh-api"
  protocol_type = "HTTP"
}

resource "aws_apigatewayv2_integration" "mesh" {
  api_id                 = aws_apigatewayv2_api.mesh.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.mesh.invoke_arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "mesh" {
  api_id    = aws_apigatewayv2_api.mesh.id
  route_key = "$default"
  target    = "integrations/${aws_apigatewayv2_integration.mesh.id}"
}

resource "aws_apigatewayv2_stage" "mesh" {
  api_id      = aws_apigatewayv2_api.mesh.id
  name        = "$default"
  auto_deploy = true
}

resource "aws_lambda_permission" "mesh_api" {
  statement_id  = "AllowApiGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.mesh.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.mesh.execution_arn}/*/*"
}
