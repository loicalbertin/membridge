# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY ./cmd ./cmd
COPY ./internal ./internal
COPY ./go.mod ./go.sum ./
RUN go mod download

FROM builder AS indexer-builder

RUN CGO_ENABLED=0 go build -o memory-indexer ./cmd/indexer

FROM builder AS mcp-builder

RUN CGO_ENABLED=0 go build -o memory-mcp ./cmd/mcp-server

# Runtime stage
FROM gcr.io/distroless/static-debian12:latest AS indexer

WORKDIR /app

COPY --from=indexer-builder /app/memory-indexer /app/memory-indexer

CMD ["./memory-indexer"]

# Runtime stage
FROM gcr.io/distroless/static-debian12:latest AS mcp

WORKDIR /app

EXPOSE 8000

COPY --from=mcp-builder /app/memory-mcp /app/memory-mcp

CMD ["./memory-mcp"]
