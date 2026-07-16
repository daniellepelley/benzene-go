# gcp-pubsub-helloworld

The `helloworld` greet handler consuming a [Google Cloud Pub/Sub push
subscription](https://cloud.google.com/pubsub/docs/push), deployed as a Cloud Run service.
Pub/Sub POSTs each message to the service's `/pubsub` endpoint; `gcppubsub.Handler` decodes
the push envelope (base64 data, attributes), resolves the topic per wire-contracts §2 (the
`topic` message attribute, or the body as a full wire envelope), and acks (204) or nacks
(500, triggering Pub/Sub's own redelivery/dead-letter machinery) based on the dispatch
result.

This is consumer-only: publishing needs no code (`gcloud pubsub topics publish` below), and
the binding's outbound half would need the Pub/Sub SDK - a dependency decision this repo
hasn't taken (see `ROADMAP.md`).

## Deploy

Requires the [gcloud CLI](https://cloud.google.com/sdk/docs/install), a GCP project with the
Cloud Run, Artifact Registry, and Pub/Sub APIs enabled, and Docker.

```
# From the repo root - the build context needs the whole module, not just this directory.
docker build -f examples/gcp-pubsub-helloworld/Dockerfile -t gcp-pubsub-helloworld .

docker tag gcp-pubsub-helloworld <region>-docker.pkg.dev/<project>/<repo>/gcp-pubsub-helloworld
docker push <region>-docker.pkg.dev/<project>/<repo>/gcp-pubsub-helloworld

gcloud run deploy gcp-pubsub-helloworld \
  --image <region>-docker.pkg.dev/<project>/<repo>/gcp-pubsub-helloworld \
  --region <region> \
  --allow-unauthenticated

# The topic and the push subscription pointing at the service:
gcloud pubsub topics create greet-helloworld
SERVICE_URL=$(gcloud run services describe gcp-pubsub-helloworld --region <region> --format 'value(status.url)')
gcloud pubsub subscriptions create greet-helloworld-push \
  --topic greet-helloworld \
  --push-endpoint "$SERVICE_URL/pubsub"
```

`--allow-unauthenticated` keeps this demo minimal. For anything real, deploy without it and
give the subscription a push auth service account instead
(`gcloud pubsub subscriptions create --push-auth-service-account=...` - Pub/Sub then attaches
an OIDC token Cloud Run verifies), which locks the endpoint to Pub/Sub without any code
change here.

## Try it

```
gcloud pubsub topics publish greet-helloworld \
  --message '{"name":"World"}' \
  --attribute topic=greet

# The observable effect is the consumer's log line in Cloud Logging:
gcloud run services logs read gcp-pubsub-helloworld --region <region> --limit 5
# ... greeted: Hello, World!
```

A message that fails (`--message '{"name":""}'`) is nacked with a 500 and redelivered per the
subscription's retry policy - watch the same logs to see the redeliveries, and add
`--dead-letter-topic` to the subscription to cap them.

## CI/CD

`.github/workflows/deploy-gcp-pubsub-helloworld.yml` runs the same build/push/deploy (plus
idempotent topic/subscription creation) on every push to `main` that touches this example.
It's gated on `secrets.GCP_SA_KEY` being set - the job is **skipped** (not failed) until you
configure:

| Name | Kind | Value |
|---|---|---|
| `GCP_SA_KEY` | secret | A service account JSON key with the Artifact Registry Writer, Cloud Run Admin, and Pub/Sub Admin roles |
| `GCP_PROJECT_ID` | variable | The target GCP project ID |
| `GCP_REGION` | variable | e.g. `us-central1` |
| `GCP_ARTIFACT_REPO` | variable | An existing Artifact Registry Docker repository in that project/region |

(The same secret/variables as `gcp-cloudrun-helloworld`, plus the Pub/Sub Admin role on the
service account.)

## What was verified in this sandbox

This sandbox has no GCP project and no reachable container registry, so nothing here was
actually deployed. What *was* verified locally:

- `go test ./examples/gcp-pubsub-helloworld/...` - the full push endpoint via
  `httptest.NewServer`: a greet message acks with 204, a failing one nacks with 500, other
  paths 404, plus `portFromEnv`'s default/override behavior (see `main_test.go`).
- `go test ./gcppubsub/...` - the binding itself against hand-built push envelopes matching
  the documented delivery format (topic attribute, envelope-in-body fallback, malformed
  payload/base64, ack/nack statuses).
- `CGO_ENABLED=0 GOOS=linux go build -o server ./examples/gcp-pubsub-helloworld` - the exact
  command the Dockerfile's build stage runs - compiles cleanly.

The deploy workflow's YAML was syntax-checked but has never actually run - it will start
running for real the first time you push to `main` after adding the secrets above. The push
*delivery format* is from Pub/Sub's documented contract, not a live subscription - the same
"verified against the documented contract" standard as the AWS and Azure examples.
