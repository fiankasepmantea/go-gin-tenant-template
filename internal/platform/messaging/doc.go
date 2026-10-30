// Package messaging defines broker-agnostic Publisher/Subscriber interfaces.
// The Kafka and RabbitMQ implementations live in sub-packages. tenant_id is always carried
// in the message headers. Status: phase 5.
package messaging
