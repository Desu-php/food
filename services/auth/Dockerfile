FROM golang:1.27.2-alpine AS build
WORKDIR /src/services/auth
ENV GOWORK=off
COPY platform /src/platform
COPY services/auth/go.mod services/auth/go.sum ./
RUN go mod download
COPY services/auth .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/auth ./cmd/api && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/authctl ./cmd/authctl

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 auth
COPY --from=build /out/auth /usr/local/bin/auth
COPY --from=build /out/authctl /usr/local/bin/authctl
USER auth
EXPOSE 8084
ENTRYPOINT ["/usr/local/bin/auth"]
