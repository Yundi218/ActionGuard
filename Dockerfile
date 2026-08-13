FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/commerce-mcp ./cmd/commerce-mcp

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build --chown=nonroot:nonroot /out/commerce-mcp /commerce-mcp
EXPOSE 8081
USER nonroot:nonroot
ENTRYPOINT ["/commerce-mcp"]
