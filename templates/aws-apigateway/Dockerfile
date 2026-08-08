# Builds the Lambda `bootstrap` binary for the provided.al2023 custom runtime.
#   docker build -t my-service .
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /out/bootstrap .

# AWS's own base image for a custom (provided.al2023) runtime - sets LAMBDA_TASK_ROOT and
# provides the runtime interface client that invokes ${LAMBDA_TASK_ROOT}/bootstrap.
FROM public.ecr.aws/lambda/provided:al2023
COPY --from=build /out/bootstrap ${LAMBDA_TASK_ROOT}/bootstrap
CMD [ "bootstrap" ]
