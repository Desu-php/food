FROM golang:1.27.2-alpine AS build
WORKDIR /src/services/delivery
ENV GOWORK=off
COPY platform /src/platform
COPY contracts /src/contracts
COPY services/delivery/go.mod services/delivery/go.sum ./
RUN go mod download
COPY services/delivery .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/delivery ./cmd/api
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/outbox ./cmd/outbox

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/consumer ./cmd/consumer

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 delivery
COPY --from=build /out/delivery /usr/local/bin/delivery
COPY --from=build /out/outbox /usr/local/bin/outbox
COPY --from=build /out/consumer /usr/local/bin/consumer
USER delivery
EXPOSE 8085
ENTRYPOINT ["/usr/local/bin/delivery"]
