# Build context MUST be the repo root (this example imports sibling packages in the module):
#   docker build -f examples/gcp-pubsub-helloworld/Dockerfile -t gcp-pubsub-helloworld .

FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/server ./examples/gcp-pubsub-helloworld

# Cloud Run's only contract is "listen on $PORT" - a minimal static base is enough.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/server /server
ENTRYPOINT ["/server"]
