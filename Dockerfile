FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /blabby-backend ./cmd/backend/ \
 && CGO_ENABLED=0 go build -o /blabby-gateway ./cmd/gateway/

FROM alpine:3.21

RUN adduser -D -u 1000 appuser

COPY --from=builder /blabby-backend /blabby-backend
COPY --from=builder /blabby-gateway /blabby-gateway

# Only the gateway serves HTTP; the backend exposes nothing.
EXPOSE 8080

USER appuser

# The image carries both binaries; the runtime (docker-compose, or `docker run`)
# selects the role, e.g. `/blabby-backend ...` or `/blabby-gateway ...`.
