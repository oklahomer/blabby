FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /blabby ./cmd/server/

FROM alpine:3.21

RUN adduser -D -u 1000 appuser

COPY --from=builder /blabby /blabby

EXPOSE 8080

USER appuser
ENTRYPOINT ["/blabby"]
