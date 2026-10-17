FROM golang:1.27.2-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/catalog ./cmd/api

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 catalog
COPY --from=build /out/catalog /usr/local/bin/catalog
USER catalog
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/catalog"]
