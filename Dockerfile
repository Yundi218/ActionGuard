FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p /app && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/api ./cmd/api && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/commerce-mcp ./cmd/commerce-mcp && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/policy-import ./cmd/policy-import

FROM alpine:3.21

RUN apk add --no-cache ca-certificates && \
    addgroup -S actionguard && adduser -S -G actionguard actionguard
COPY --from=build --chown=actionguard:actionguard /app /app
EXPOSE 8080 8081
USER actionguard:actionguard
