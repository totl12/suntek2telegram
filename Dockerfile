FROM --platform=$BUILDPLATFORM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG version
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -a -ldflags "-w -s -X main.version=$version -extldflags '-static'" \
    -o suntek2telegram ./cmd/suntek2telegram/main.go

FROM alpine:3
RUN apk add --no-cache ca-certificates && mkdir -p /data/photos

COPY --from=builder /app/suntek2telegram /suntek2telegram

VOLUME ["/data"]

# Web UI
EXPOSE 8080

ENTRYPOINT ["/suntek2telegram", "-conf", "/config.yml"]
