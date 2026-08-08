# Build context MUST be the repo root (this example imports sibling packages in the module),
# e.g.: docker build -f examples/aws-dynamodb-helloworld/Dockerfile -t aws-dynamodb-helloworld .

FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /out/bootstrap ./examples/aws-dynamodb-helloworld

# AWS's own base image for a custom (provided.al2023) runtime - sets LAMBDA_TASK_ROOT and
# provides the runtime interface client that invokes ${LAMBDA_TASK_ROOT}/bootstrap.
FROM public.ecr.aws/lambda/provided:al2023
COPY --from=build /out/bootstrap ${LAMBDA_TASK_ROOT}/bootstrap
CMD [ "bootstrap" ]
