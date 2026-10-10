FROM golang:1.22-alpine AS build

WORKDIR /app
COPY go.mod go.sum ./
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -o /control-plane ./cmd/server

FROM alpine:3.20

RUN apk add --no-cache wget

WORKDIR /app
COPY --from=build /control-plane /usr/local/bin/control-plane

EXPOSE 8081

ENTRYPOINT ["/usr/local/bin/control-plane"]
