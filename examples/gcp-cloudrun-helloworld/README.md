# gcp-cloudrun-helloworld

The `helloworld` greet handler deployed to [Google Cloud
Run](https://cloud.google.com/run/docs). Cloud Run's entire contract is "listen on `$PORT`" -
which `httpbinding.Handler` + `net/http.ListenAndServe` already does, so **this example needs no
Google-specific Go package at all**, unlike the AWS and Azure examples.

## Why Cloud Run and not Cloud Functions

Cloud Functions (2nd gen) is [built on Cloud Run under the
hood](https://cloud.google.com/functions/docs/concepts/execution-environment) - deploying
straight to Cloud Run gets the same autoscaling/scale-to-zero behavior with one fewer layer, and
without needing the `functions-framework-go` dependency that `gcloud functions deploy`'s
buildpack requires for Go (the one Google-specific dependency this port has avoided everywhere
else). If you specifically want `gcloud functions deploy --runtime go1XX`, the constraint is
just that dependency plus an exported handler function matching its expected signature - not
included here to keep this repo's zero-third-party-dependency policy intact.

## Deploy

Requires the [gcloud CLI](https://cloud.google.com/sdk/docs/install), a GCP project with the
Cloud Run and Artifact Registry APIs enabled, and Docker.

```
# From the repo root - the build context needs the whole module, not just this directory.
docker build -f examples/gcp-cloudrun-helloworld/Dockerfile -t gcp-cloudrun-helloworld .

docker tag gcp-cloudrun-helloworld <region>-docker.pkg.dev/<project>/<repo>/gcp-cloudrun-helloworld
docker push <region>-docker.pkg.dev/<project>/<repo>/gcp-cloudrun-helloworld

gcloud run deploy gcp-cloudrun-helloworld \
  --image <region>-docker.pkg.dev/<project>/<repo>/gcp-cloudrun-helloworld \
  --region <region> \
  --allow-unauthenticated
```

(`gcloud run deploy --source .` is a common shortcut elsewhere, but it uses its source
directory both as the Docker build context *and* where it looks for a Dockerfile - since this
example's Dockerfile needs the whole module as context, the explicit build/push/deploy above is
what actually works here.)

## CI/CD

`.github/workflows/deploy-gcp-cloudrun-helloworld.yml` runs the same build/push/deploy on every
push to `main` that touches this example. It's gated on `secrets.GCP_SA_KEY` being set - the
job is **skipped** (not failed) until you configure:

| Name | Kind | Value |
|---|---|---|
| `GCP_SA_KEY` | secret | A service account JSON key with the Artifact Registry Writer and Cloud Run Admin roles |
| `GCP_PROJECT_ID` | variable | The target GCP project ID |
| `GCP_REGION` | variable | e.g. `us-central1` |
| `GCP_ARTIFACT_REPO` | variable | An existing Artifact Registry Docker repository in that project/region |

## Try it

```
curl -X POST "$SERVICE_URL/greet" -d '{"name":"World"}'
# {"greeting":"Hello, World!"}

curl -X POST "$SERVICE_URL/greet" -d '{"name":""}'
# 400 Bad Request
```

## What was verified in this sandbox

This sandbox has no GCP project and no reachable container registry, so nothing here was
actually deployed. What *was* verified locally:

- `go test ./examples/gcp-cloudrun-helloworld/...` - the full HTTP server via
  `httptest.NewServer`, including the failure path and `portFromEnv`'s default/override
  behavior (see `main_test.go`).
- `CGO_ENABLED=0 GOOS=linux go build -o server ./examples/gcp-cloudrun-helloworld` - the exact
  command the Dockerfile's build stage runs - compiles cleanly.

The deploy workflow's YAML was syntax-checked but has never actually run - it will start
running for real the first time you push to `main` after adding the secrets above.
