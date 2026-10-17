FROM golang:1.27.2-alpine AS build
WORKDIR /src/services/orders
ENV GOWORK=off
COPY contracts /src/contracts
COPY services/orders/go.mod services/orders/go.sum ./
RUN go mod download
COPY services/orders .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/orders ./cmd/api
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/outbox ./cmd/outbox

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/consumer ./cmd/consumer

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/saga ./cmd/saga

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 orders
COPY --from=build /out/orders /usr/local/bin/orders
COPY --from=build /out/outbox /usr/local/bin/outbox
COPY --from=build /out/consumer /usr/local/bin/consumer
COPY --from=build /out/saga /usr/local/bin/saga
USER orders
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/orders"]
