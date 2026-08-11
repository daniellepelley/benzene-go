# Build context MUST be the repo root (this example imports sibling modules via go.work):
#   docker build -f examples/k8s-helloworld/Dockerfile -t k8s-helloworld:local .

FROM golang:1.24 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/k8s-helloworld ./examples/k8s-helloworld

# Serves HTTP AND polls SQS AND consumes Kafka - one process, one binary, one minimal static base.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/k8s-helloworld /k8s-helloworld
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/k8s-helloworld"]
