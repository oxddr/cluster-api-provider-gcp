FROM golang:1.10.0 as builder
WORKDIR /go/src/sigs.k8s.io/cluster-api-provider-gcp
# This expects that the context passed to the docker build command is
# the cluster-api directory.
# e.g. docker build -t <tag> -f <this_Dockerfile> <path_to_cluster-api>
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go install -a -ldflags '-extldflags "-static"' sigs.k8s.io/cluster-api-provider-gcp/cmd/controller-manager

# Final container
FROM debian:stretch-slim
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /go/bin/controller-controller
