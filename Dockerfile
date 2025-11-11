# Use Go image as builder
FROM --platform=$BUILDPLATFORM golang:1 AS builder

# Arguments for target OS and architecture
ARG TARGETOS
ARG TARGETARCH

# Set the working directory
WORKDIR /src

# Copy the manifests and install dependencies
COPY go.mod go.sum ./
RUN --mount=type=cache,sharing=private,target=/go/pkg/mod \
    go mod download

# Copy the rest of files and build the binary
COPY . ./
RUN --mount=type=cache,sharing=private,target=/go/pkg/mod \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/ferron-ingress .

# Use Ferron 2 image as base image
FROM ferronserver/ferron:2

# Copy the binary from the builder stage
COPY --from=builder /out/ferron-ingress /usr/local/bin/ferron-ingress

# Set the entrypoint
ENTRYPOINT ["/usr/local/bin/ferron-ingress"]
