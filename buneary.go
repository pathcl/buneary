package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	rabbithole "github.com/michaelklishin/rabbit-hole/v2"
	"github.com/streadway/amqp"
)

const (
	amqpDefaultPort = 5672
	apiDefaultPort  = 15672
)

type (
	// ExchangeType represents the type of an exchange and thus defines its routing
	// behavior. The type cannot be changed after the exchange has been created.
	ExchangeType string

	// QueueType represents the type of a queue.
	QueueType string

	// BindingType represents the type of a binding and determines whether it binds
	// to a queue - which is the default case - or to another exchange.
	BindingType string
)

const (
	// Direct will deliver messages to queues based on their routing key. A direct
	// exchange compares the routing key to all registered binding keys and forwards
	// the message to all queues with matching binding keys.
	Direct ExchangeType = "direct"

	// Headers will deliver messages to queues based on their headers. This exchange
	// type will ignore the actual routing key.
	Headers = "headers"

	// Fanout will deliver messages to all bound queues of an exchange and ignore
	// the routing key, making them suitable for broadcasting scenarios.
	Fanout = "fanout"

	// Topic will deliver messages to queues based on a binding pattern. The exchange
	// will compare the routing key to all queue binding patterns and forward the
	// message to all matching queues.
	Topic = "topic"

	// Classic represents a classic message queue without any particularities.
	Classic QueueType = "classic"

	// Quorum represents a quorum queue.
	Quorum = "quorum"

	// ToQueue represents a binding from an exchange to a queue.
	ToQueue BindingType = "queue"

	// ToExchange represents a binding from an exchange to another exchange.
	ToExchange = "exchange"
)

// Provider prescribes all functions a buneary implementation has to possess.
type Provider interface {

	// CreateExchange creates a new exchange. If an exchange with the provided name
	// already exists, nothing will happen.
	CreateExchange(exchange Exchange) error

	// CreateQueue will create a new queue. If a queue with the provided name
	// already exists, nothing will happen. CreateQueue will return the queue
	// name generated by the server if no name has been provided.
	CreateQueue(queue Queue) (string, error)

	// CreateBinding will create a new binding. If a binding with the provided
	// target already exists, nothing will happen.
	CreateBinding(binding Binding) error

	// GetExchanges returns all exchanges that pass the provided filter function.
	// To get all exchanges, pass a filter function that always returns true.
	GetExchanges(filter func(exchange Exchange) bool) ([]Exchange, error)

	// GetQueues returns all queues that pass the provided filter function. To get
	// all queues, pass a filter function that always returns true.
	GetQueues(filter func(queue Queue) bool) ([]Queue, error)

	// GetBindings returns all bindings that pass the provided filter function. To
	// get all bindings, pass a filter function that always returns true.
	GetBindings(filter func(binding Binding) bool) ([]Binding, error)

	// GetMessages reads max messages from the given queue. The messages will be
	// re-queued if requeue is set to true. Otherwise, they will be removed from
	// the queue and thus won't be read by subscribers.
	//
	// This behavior may not be obvious to the user, especially if they merely
	// want to "take a look" into the queue without altering its state. Therefore,
	// an implementation should require the user opt-in to this behavior.
	GetMessages(queue Queue, max int, requeue bool) ([]Message, error)

	// PublishMessage publishes a message to the given exchange. The exchange
	// has to exist or must be created before the message is published.
	//
	// The actual message routing is defined by the exchange type. If no routing
	// key is given, the message will be sent to the default exchange.
	PublishMessage(message Message) error

	// DeleteExchange deletes the given exchange from the server. Will return
	// an error if the specified exchange name doesn't exist.
	DeleteExchange(exchange Exchange) error

	// DeleteQueue deletes the given queue from the server. Will return an error
	// if the specified queue name doesn't exist.
	DeleteQueue(queue Queue) error
}

// RabbitMQConfig stores RabbitMQ-related configuration values.
type RabbitMQConfig struct {

	// Address specifies the RabbitMQ address in the form `localhost:5672`. The
	// port is not mandatory. If there's no port, 5672 will be used as default.
	Address string

	// User represents the username for setting up a connection.
	User string

	// Password represents the password to authenticate with.
	Password string
}

// URI returns the AMQP URI for a configuration, prefixed with amqp://.
// In case the RabbitMQ address lacks a port, the default port will be used.
func (a *RabbitMQConfig) URI() string {
	tokens := strings.Split(a.Address, ":")
	var port string

	if len(tokens) == 2 {
		port = tokens[1]
	} else {
		port = strconv.Itoa(amqpDefaultPort)
	}

	uri := fmt.Sprintf("amqp://%s:%s@%s:%s", a.User, a.Password, tokens[0], port)

	return uri
}

// apiURI returns the URI for the RabbitMQ HTTP API, prefixed with http://. In case
// the RabbitMQ server address lacks a port, the default port will be used.
func (a *RabbitMQConfig) apiURI() string {
	tokens := strings.Split(a.Address, ":")
	var port string

	if len(tokens) == 2 {
		port = tokens[1]
	} else {
		port = strconv.Itoa(apiDefaultPort)
	}

	uri := fmt.Sprintf("https://%s:%s", tokens[0], port)

	return uri
}

// Exchange represents a RabbitMQ exchange.
type Exchange struct {

	// Name is the name of the exchange. Names starting with `amq.` denote pre-
	// defined exchanges and should be avoided. A valid name is not empty and only
	// contains letters, digits, hyphens, underscores, periods and colons.
	Name string

	// Type is the type of the exchange and determines in which fashion messages are
	// routed by the exchanged. It cannot be changed afterwards.
	Type ExchangeType

	// Durable determines whether the exchange will be persisted, i.e. be available
	// after server restarts. By default, an exchange is not durable.
	Durable bool

	// AutoDelete determines whether the exchange will be deleted automatically once
	// there are no bindings to any queues left. It won't be deleted by default.
	AutoDelete bool

	// Internal determines whether the exchange should be public-facing or not.
	Internal bool

	// NoWait determines whether the client should wait for the server confirming
	// operations related to the passed exchange. For instance, if NoWait is set to
	// false when creating an exchange, the client won't wait for confirmation.
	NoWait bool
}

// Queue represents a message queue.
type Queue struct {

	// Name is the name of the queue. The name might be empty, in which case the
	// RabbitMQ server will generate and return a name for the queue. Queue names
	// follow the same rules as exchange names regarding the valid characters.
	Name string

	// Type is the type of the queue. Most users will only need classic queues, but
	// buneary strives to support quorum queues as well.
	//
	// For more information, see https://www.rabbitmq.com/quorum-queues.html.
	Type QueueType

	// Durable determines whether the queue will be persisted, i.e. be available after
	// server restarts. By default, an queue is not durable.
	Durable bool

	// AutoDelete determines whether the queue will be deleted automatically once
	// there are no consumers to ready from it left. It won't be deleted by default.
	AutoDelete bool

	// Amount of messages in a queue
	Messages int

	// Leader Node for Queue
	Node string

	// Messages unacknowledged for Queue
	MessagesUnAck int
}

// Binding represents an exchange- or queue binding.
type Binding struct {

	// Type is the type of the binding and determines whether the exchange binds to
	// another exchange or to a queue. Depending on the binding type, the server will
	// look for an exchange or queue with the provided target name.
	Type BindingType

	// From is the "source" of a binding going to the target. Even though this is an
	// Exchange instance, only the exchange name is needed for creating a binding.
	//
	// To bind to a durable queue, the source exchange has to be durable as well. This
	// won't be checked on client-side, but an error will be returned by the server if
	// this constraint is not met.
	From Exchange

	// TargetName is the name of the target, which is either an exchange or a queue.
	TargetName string

	// Key is the key of the binding. The key is crucial for message routing from the
	// exchange to the bound queue or to another exchange.
	Key string
}

// Message represents a message to be enqueued.
type Message struct {

	// Target is the target exchange. Even though this is an entire Exchange instance,
	// only the exchange name is required for sending a message.
	Target Exchange

	// Headers represents the message headers, which is a set of arbitrary key-value
	// pairs. Message headers are considered by some exchange types and thus can be
	// relevant for message routing.
	Headers map[string]interface{}

	// RoutingKey is the routing key of the message and largely determines how the
	// message will be routed and which queues will receive the message. See the
	// individual ExchangeType constants for more information on routing behavior.
	RoutingKey string

	// Body represents the message body.
	Body []byte
}

// NewProvider initializes and returns a default Provider instance.
func NewProvider(config *RabbitMQConfig) Provider {
	b := buneary{
		config: config,
	}
	return &b
}

// buneary is an implementation of the Provider interface with sane defaults.
type buneary struct {
	config  *RabbitMQConfig
	channel *amqp.Channel
	client  *rabbithole.Client
}

// setupChannel dials the configured RabbitMQ server, sets up a connection and opens a
// channel from that connection, which should be closed once buneary has finished.
func (b *buneary) setupChannel() error {
	if b.channel != nil {
		if err := b.channel.Close(); err != nil {
			return fmt.Errorf("closing AMQP channel: %w", err)
		}
	}

	conn, err := amqp.Dial(b.config.URI())
	if err != nil {
		return fmt.Errorf("dialling RabbitMQ server: %w", err)
	}

	if b.channel, err = conn.Channel(); err != nil {
		return fmt.Errorf("establishing AMQP channel: %w", err)
	}

	return nil
}

// setupClient establishes a connection to the RabbitMQ HTTP API, initializing the
// rabbit-hole client. It requires all connection data to exist in the configuration.
func (b *buneary) setupClient() error {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	client, err := rabbithole.NewTLSClient(b.config.apiURI(), b.config.User, b.config.Password, transport)
	if err != nil {
		return fmt.Errorf("creating rabbit-hole client: %w", err)
	}
	b.client = client

	return nil
}

// CreateExchange creates the given exchange. See Provider.CreateExchange for details.
func (b *buneary) CreateExchange(exchange Exchange) error {
	if err := b.setupClient(); err != nil {
		return err
	}

	_, err := b.client.DeclareExchange("/", exchange.Name, rabbithole.ExchangeSettings{
		Type:       string(exchange.Type),
		Durable:    exchange.Durable,
		AutoDelete: exchange.AutoDelete,
	})
	if err != nil {
		return fmt.Errorf("declaring exchange: %w", err)
	}

	return nil
}

// CreateQueue creates the given queue. See Provider.CreateQueue for details.
func (b *buneary) CreateQueue(queue Queue) (string, error) {
	if err := b.setupClient(); err != nil {
		return "", err
	}

	// ToDo: Fetch and return the generated queue name from the response.
	_, err := b.client.DeclareQueue("/", queue.Name, rabbithole.QueueSettings{
		Type:       string(queue.Type),
		Durable:    queue.Durable,
		AutoDelete: queue.AutoDelete,
	})
	if err != nil {
		return "", fmt.Errorf("declaring queue: %w", err)
	}

	return "", nil
}

// CreateBinding creates the given binding. See Provider.CreateBinding for details.
func (b *buneary) CreateBinding(binding Binding) error {
	if err := b.setupClient(); err != nil {
		return err
	}

	_, err := b.client.DeclareBinding("/", rabbithole.BindingInfo{
		Source:          binding.From.Name,
		Vhost:           "/",
		Destination:     binding.TargetName,
		DestinationType: string(binding.Type),
		RoutingKey:      binding.Key,
	})
	if err != nil {
		return fmt.Errorf("declaring binding: %w", err)
	}

	return nil
}

// GetExchanges returns exchanges passing the filter. See Provider.GetExchanges for details.
func (b *buneary) GetExchanges(filter func(exchange Exchange) bool) ([]Exchange, error) {
	if err := b.setupClient(); err != nil {
		return nil, err
	}

	exchangeInfos, err := b.client.ListExchanges()
	if err != nil {
		return nil, fmt.Errorf("listing exchanges: %w", err)
	}

	var exchanges []Exchange

	for _, info := range exchangeInfos {
		e := Exchange{
			Name:       info.Name,
			Type:       ExchangeType(info.Type),
			Durable:    info.Durable,
			AutoDelete: info.AutoDelete,
			Internal:   info.Internal,
		}

		if filter(e) {
			exchanges = append(exchanges, e)
		}
	}

	return exchanges, nil
}

// GetQueues returns queues passing the filter. See Provider.GetQueues for details.
func (b *buneary) GetQueues(filter func(queue Queue) bool) ([]Queue, error) {
	if err := b.setupClient(); err != nil {
		return nil, err
	}

	queueInfos, err := b.client.ListQueues()
	if err != nil {
		return nil, fmt.Errorf("listing queues: %w", err)
	}

	var queues []Queue

	for _, info := range queueInfos {
		q := Queue{
			Name:          info.Name,
			Durable:       info.Durable,
			AutoDelete:    info.AutoDelete,
			Messages:      info.Messages,
			MessagesUnAck: info.MessagesUnacknowledged,
			Node:          info.Node,
		}

		if filter(q) {
			queues = append(queues, q)
		}
	}

	return queues, nil
}

// GetBindings returns bindings passing the filter. See Provider.GetBindings for details.
func (b *buneary) GetBindings(filter func(binding Binding) bool) ([]Binding, error) {
	if err := b.setupClient(); err != nil {
		return nil, err
	}

	bindingInfos, err := b.client.ListBindings()
	if err != nil {
		return nil, fmt.Errorf("listing bindings: %w", err)
	}

	var bindings []Binding

	for _, info := range bindingInfos {
		b := Binding{
			Type:       BindingType(info.DestinationType),
			From:       Exchange{Name: info.Source},
			TargetName: info.Destination,
			Key:        info.RoutingKey,
		}

		if filter(b) {
			bindings = append(bindings, b)
		}
	}

	return bindings, nil
}

// GetMessages reads messages from the given queue. See Provider.GetMessages for details.
//
// ToDo: Maybe move the function-scoped types somewhere else.
func (b *buneary) GetMessages(queue Queue, max int, requeue bool) ([]Message, error) {
	// getMessagesRequestBody represents the HTTP request body for reading messages.
	type getMessagesRequestBody struct {
		Count    int    `json:"count"`
		Requeue  bool   `json:"requeue"`
		Encoding string `json:"encoding"`
		Ackmode  string `json:"ackmode"`
	}

	// getMessagesRequestBody represents the HTTP response body returned by the RabbitMQ
	// API endpoint for reading messages from a queue (/api/queues/vhost/name/get).
	type getMessagesResponseBody []struct {
		PayloadBytes int                    `json:"payload_bytes"`
		Redelivered  bool                   `json:"redelivered"`
		Exchange     string                 `json:"exchange"`
		RoutingKey   string                 `json:"routing_key"`
		Headers      map[string]interface{} `json:"headers"`
		Payload      string                 `json:"payload"`
	}

	requestBody := getMessagesRequestBody{
		Count:    max,
		Requeue:  requeue,
		Encoding: "auto",
		Ackmode:  "ack_requeue_true",
	}

	requestBodyJson, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("marshalling request body: %w", err)
	}

	uri := fmt.Sprintf("%s/api/queues/%%2F/%s/get", b.config.apiURI(), queue.Name)

	request, err := http.NewRequest("POST", uri, bytes.NewReader(requestBodyJson))
	if err != nil {
		return nil, fmt.Errorf("creating POST request: %w", err)
	}

	request.SetBasicAuth(b.config.User, b.config.Password)

	response, err := (&http.Client{}).Do(request)
	if err != nil {
		return nil, err
	}

	if response.StatusCode != 200 {
		return nil, fmt.Errorf("RabbitMQ server returned non-200 status: %s", response.Status)
	}

	defer func() {
		_ = response.Body.Close()
	}()

	responseBody := getMessagesResponseBody{}

	if err := json.NewDecoder(response.Body).Decode(&responseBody); err != nil {
		return nil, err
	}

	messages := make([]Message, len(responseBody))

	for i, m := range responseBody {
		messages[i] = Message{
			Target:     Exchange{Name: m.Exchange},
			Headers:    m.Headers,
			RoutingKey: m.RoutingKey,
			Body:       []byte(m.Payload),
		}
	}

	return messages, nil
}

// PublishMessage publishes the given message. See Provider.PublishMessage for details.
func (b *buneary) PublishMessage(message Message) error {
	if err := b.setupChannel(); err != nil {
		return err
	}

	defer func() {
		_ = b.Close()
	}()

	if err := b.channel.Publish(messageArgs(message)); err != nil {
		return fmt.Errorf("publishing message: %w", err)
	}

	return nil
}

// DeleteExchange deletes the given exchange. See Provider.DeleteExchange for details.
func (b *buneary) DeleteExchange(exchange Exchange) error {
	if err := b.setupClient(); err != nil {
		return err
	}

	_, err := b.client.DeleteExchange("/", exchange.Name)
	if err != nil {
		return fmt.Errorf("deleting exchange: %w", err)
	}

	return nil
}

// DeleteQueue deletes the given exchange. See Provider.DeleteQueue for details.
func (b *buneary) DeleteQueue(queue Queue) error {
	if err := b.setupClient(); err != nil {
		return err
	}

	_, err := b.client.DeleteQueue("/", queue.Name)
	if err != nil {
		return fmt.Errorf("deleting queue: %w", err)
	}

	return nil
}

// Close closes the AMQP channel to the configured RabbitMQ server. This function
// should be called after running PublishMessage.
func (b *buneary) Close() error {
	if b.channel != nil {
		if err := b.channel.Close(); err != nil {
			return fmt.Errorf("closing AMQP channel: %w", err)
		}
	}

	return nil
}

// messageArgs returns all message fields expected by the AMQP library as single
// values. This avoids large parameter lists when calling library functions.
func messageArgs(message Message) (string, string, bool, bool, amqp.Publishing) {
	return message.Target.Name,
		message.RoutingKey,
		false,
		false,
		amqp.Publishing{
			Headers:   message.Headers,
			Timestamp: time.Now(),
			Body:      message.Body,
		}
}
