ARG PLUGIN_VERSION=4.0.7
ARG BASE_VERSION=4.0.7

FROM alpine:3.18 AS builder

ARG PLUGIN_VERSION

RUN apk add --no-cache curl

RUN mkdir -p /plugins && \
    curl -fsSL \
    -o "/plugins/rabbitmq_delayed_message_exchange-${PLUGIN_VERSION}.ez" \
    https://github.com/rabbitmq/rabbitmq-delayed-message-exchange/releases/download/v${PLUGIN_VERSION}/rabbitmq_delayed_message_exchange-v${PLUGIN_VERSION}.ez

FROM rabbitmq:${BASE_VERSION}-management-alpine

ARG PLUGIN_VERSION

COPY --from=builder --chown=rabbitmq:rabbitmq \
    /plugins/rabbitmq_delayed_message_exchange-${PLUGIN_VERSION}.ez \
    ${RABBITMQ_HOME}/plugins/

RUN rabbitmq-plugins enable --offline rabbitmq_delayed_message_exchange \
    && rabbitmq-plugins enable --offline rabbitmq_consistent_hash_exchange
