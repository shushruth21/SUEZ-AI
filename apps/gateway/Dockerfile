FROM golang:1.22-alpine AS build

WORKDIR /app
COPY go.mod go.sum ./
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -o /gateway ./cmd/server

FROM alpine:3.20

RUN apk add --no-cache wget

WORKDIR /app
COPY --from=build /gateway /usr/local/bin/gateway

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/gateway"]
