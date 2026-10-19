#!/bin/sh
set -eu
# Existing topics keep their partition count: changing it changes key routing.
if ! rpk topic describe "$ORDERS_EVENTS_TOPIC" -X "brokers=$KAFKA_BROKERS" >/dev/null 2>&1; then
 rpk topic create "$ORDERS_EVENTS_TOPIC" -p "$ORDERS_EVENTS_PARTITIONS" -r "$ORDERS_EVENTS_REPLICAS" \
  -c "retention.ms=$ORDERS_EVENTS_RETENTION_MS" -c cleanup.policy=delete -X "brokers=$KAFKA_BROKERS"
fi
rpk topic describe "$ORDERS_EVENTS_TOPIC" -X "brokers=$KAFKA_BROKERS"

if ! rpk topic describe "$DELIVERY_EVENTS_TOPIC" -X "brokers=$KAFKA_BROKERS" >/dev/null 2>&1; then
 rpk topic create "$DELIVERY_EVENTS_TOPIC" -p "$ORDERS_EVENTS_PARTITIONS" -r "$ORDERS_EVENTS_REPLICAS" \
  -c "retention.ms=$ORDERS_EVENTS_RETENTION_MS" -c cleanup.policy=delete -X "brokers=$KAFKA_BROKERS"
fi
rpk topic describe "$DELIVERY_EVENTS_TOPIC" -X "brokers=$KAFKA_BROKERS"

if ! rpk topic describe "$DELIVERY_COMMANDS_TOPIC" -X "brokers=$KAFKA_BROKERS" >/dev/null 2>&1; then
 rpk topic create "$DELIVERY_COMMANDS_TOPIC" -p "$ORDERS_EVENTS_PARTITIONS" -r "$ORDERS_EVENTS_REPLICAS" \
  -c "retention.ms=$ORDERS_EVENTS_RETENTION_MS" -c cleanup.policy=delete -X "brokers=$KAFKA_BROKERS"
fi
rpk topic describe "$DELIVERY_COMMANDS_TOPIC" -X "brokers=$KAFKA_BROKERS"

for topic in courier-offers.events.v1 fulfillment.events.v1; do
 if ! rpk topic describe "$topic" -X "brokers=$KAFKA_BROKERS" >/dev/null 2>&1; then
  rpk topic create "$topic" -p "$ORDERS_EVENTS_PARTITIONS" -r "$ORDERS_EVENTS_REPLICAS" -c "retention.ms=$ORDERS_EVENTS_RETENTION_MS" -c cleanup.policy=delete -X "brokers=$KAFKA_BROKERS"
 fi
 rpk topic describe "$topic" -X "brokers=$KAFKA_BROKERS"
done
