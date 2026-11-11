FROM golang:1.26.11-alpine AS build

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /gateway ./cmd/server

FROM alpine:3.24

# wget is used by the container health check only.
RUN apk add --no-cache wget ca-certificates && adduser -D -H -u 10001 suez

COPY --from=build /gateway /usr/local/bin/gateway

# Never run as root inside the container.
USER 10001:10001
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/gateway"]
