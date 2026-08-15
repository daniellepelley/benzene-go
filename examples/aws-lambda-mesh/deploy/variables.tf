variable "region" {
  description = "AWS region to deploy into."
  type        = string
  default     = "eu-west-1"
}

variable "project" {
  description = "Name prefix for every resource this stack creates."
  type        = string
  default     = "benzene-go-lambda-mesh"
}

variable "discovery_tag_key" {
  description = <<-EOT
    The resource tag key the six service Lambdas carry (the mesh Lambda deliberately does not).
    Kept for cross-language parity with benzene-dotnet's/benzene-typescript's AwsMesh examples,
    whose mesh discovers services by this tag (ListFunctions + ListTags) - this port's mesh does
    no AWS-side discovery at all (see ../README.md's "Divergence from .NET" section), so nothing
    here reads the tag; it exists purely as documentation, the same stance
    examples/k8s-mesh-helloworld's k8s manifests take on their own unused "benzene" Service label.
  EOT
  type        = string
  default     = "benzene"
}

variable "lambda_architecture" {
  description = "Lambda architecture (arm64 or x86_64). Must match the GOARCH the zips in deploy/build were cross-compiled for (see deploy/build.sh)."
  type        = string
  default     = "arm64"
}

variable "mesh_reserved_concurrency" {
  description = <<-EOT
    reserved_concurrent_executions pinned on the mesh Lambda. meshd.Collector's fleet state is
    in-memory and per execution environment - concurrent warm instances would each see a
    different, partial fleet, and a cold start forgets everything. Pinning this to 1 makes the
    demo deterministic (one collector instance, one state) at the cost of horizontal scalability -
    a documented, deliberate tradeoff (see ../README.md's "Divergence from .NET" section), not a
    production posture. Do not raise it without also replacing the in-memory store with something
    shared.
  EOT
  type        = number
  default     = 1
}

variable "aws_lambda_managed_runtime" {
  description = "The custom Lambda runtime the Go bootstrap binaries target - see docs/lambda for provided.al2023's contract."
  type        = string
  default     = "provided.al2023"
}

# Paths to the built Lambda zips (each a single `bootstrap` binary for the custom runtime).
# Produced by `deploy/build.sh` (GOOS=linux GOARCH=<lambda_architecture> go build), which writes
# them to deploy/build. Defaults assume that layout.
variable "orders_zip" {
  type    = string
  default = "build/orders.zip"
}
variable "payments_zip" {
  type    = string
  default = "build/payments.zip"
}
variable "shipping_zip" {
  type    = string
  default = "build/shipping.zip"
}
variable "inventory_zip" {
  type    = string
  default = "build/inventory.zip"
}
variable "notifications_zip" {
  type    = string
  default = "build/notifications.zip"
}
variable "analytics_zip" {
  type    = string
  default = "build/analytics.zip"
}
variable "mesh_zip" {
  type    = string
  default = "build/mesh.zip"
}
