FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/search-gateway ./cmd/search-gateway

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 65532 gateway
COPY --from=build /out/search-gateway /usr/local/bin/search-gateway
RUN mkdir -p /etc/search-gateway /data && chown -R gateway:gateway /data
USER gateway
WORKDIR /data
EXPOSE 8787
ENTRYPOINT ["search-gateway"]
CMD ["-config", "/etc/search-gateway/config.json"]
