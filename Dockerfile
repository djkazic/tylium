FROM golang:1.24-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /usr/local/bin/tyld ./cmd/tyld
RUN CGO_ENABLED=0 go build -o /usr/local/bin/tylminer ./cmd/tylminer
RUN CGO_ENABLED=0 go build -o /usr/local/bin/tylcli ./cmd/tylcli
RUN CGO_ENABLED=0 go build -o /usr/local/bin/tylpush ./cmd/tylpush
RUN CGO_ENABLED=0 go build -o /usr/local/bin/tylbuild ./cmd/tylbuild

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /usr/local/bin/tyld /usr/local/bin/tyld
COPY --from=builder /usr/local/bin/tylminer /usr/local/bin/tylminer
COPY --from=builder /usr/local/bin/tylcli /usr/local/bin/tylcli
COPY --from=builder /usr/local/bin/tylpush /usr/local/bin/tylpush
COPY --from=builder /usr/local/bin/tylbuild /usr/local/bin/tylbuild

EXPOSE 19332
VOLUME ["/data"]

ENTRYPOINT ["tyld"]
CMD ["--genesis", "1", "--datadir", "/data", "--rpcaddr", ":19332"]
