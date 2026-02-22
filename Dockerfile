FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/rds-iam-proxy ./cmd/rds-iam-proxy

FROM alpine:3.22
RUN adduser -D -u 10001 appuser
USER appuser
WORKDIR /app
COPY --from=builder /out/rds-iam-proxy /usr/local/bin/rds-iam-proxy
ENTRYPOINT ["/usr/local/bin/rds-iam-proxy"]
