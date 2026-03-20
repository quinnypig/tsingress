FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /tsingress .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates && \
    adduser -D -H -s /sbin/nologin tsingress
COPY --from=builder /tsingress /usr/local/bin/tsingress
USER tsingress
EXPOSE 443 80
VOLUME ["/var/lib/tsingress"]
ENTRYPOINT ["tsingress"]
CMD ["-config", "/etc/tsingress/tsingress.yaml"]
