FROM golang:1.27.2-alpine AS build
WORKDIR /src/services/notifications
ENV GOWORK=off
COPY contracts /src/contracts
COPY platform /src/platform
COPY services/notifications/go.mod services/notifications/go.sum ./
RUN go mod download
COPY services/notifications .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/notifications ./cmd/consumer
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/notification-push-worker ./cmd/push-worker

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 notifications
COPY --from=build /out/notifications /usr/local/bin/notifications
COPY --from=build /out/notification-push-worker /usr/local/bin/notification-push-worker
USER notifications
EXPOSE 8083
ENTRYPOINT ["/usr/local/bin/notifications"]
