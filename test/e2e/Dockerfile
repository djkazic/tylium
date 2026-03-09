FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /e2e ./test/e2e

FROM alpine:3.21
COPY --from=builder /e2e /e2e
ENTRYPOINT ["/e2e"]
