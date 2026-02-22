## Build stage
FROM golang:1.24 AS builder

WORKDIR /workspace

# Copy module files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -o agent-access-controller ./cmd/main.go

## Runtime stage
FROM alpine:3.21

WORKDIR /

COPY --from=builder /workspace/agent-access-controller .

ENTRYPOINT ["/agent-access-controller"]
