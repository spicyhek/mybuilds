// Package queue drains the off-cluster ingestion buffer (plan section 8.6):
// the SQS queue that the Cloudflare Worker in worker/ fills with GitHub, CI
// and Argo CD deliveries. Each message goes through the same Ingester as the
// direct endpoints, signature check included, and is deleted once it is
// stored, a duplicate, ignored, or rejected for good. A message that cannot
// be stored comes back after its visibility timeout; the queue parks it in
// the dead-letter queue after five tries.
package queue

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/spicyhek/mybuilds/internal/ingest"
)

// Envelope is one queued delivery, as the Worker writes it.
type Envelope struct {
	Provider string `json:"provider"`
	// Headers are the request headers mybuilds reads, lower-case.
	Headers map[string]string `json:"headers"`
	// BodyBase64 is the raw request body, so the signature is checked over
	// the same bytes the sender signed.
	BodyBase64 string    `json:"body_base64"`
	ReceivedAt time.Time `json:"received_at"`
}

// API is the part of the SQS client the consumer uses.
type API interface {
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessageBatch(ctx context.Context, in *sqs.DeleteMessageBatchInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
}

// Options configures a Consumer.
type Options struct {
	QueueURL string
	// Region is read from QueueURL when empty.
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	Ingester        *ingest.Ingester
	Logger          *slog.Logger
	Registerer      prometheus.Registerer
	// Client replaces the SQS client, for tests.
	Client API
	// WaitTime is the long-poll wait, 20 seconds by default.
	WaitTime time.Duration
}

const (
	defaultWaitTime   = 20 * time.Second
	visibilityTimeout = 60 * time.Second
	maxMessages       = 10
	maxBackoff        = 30 * time.Second
)

// Consumer long-polls the queue and ingests what it holds.
type Consumer struct {
	queueURL string
	client   API
	ingester *ingest.Ingester
	logger   *slog.Logger
	wait     time.Duration

	messages      *prometheus.CounterVec
	receiveErrors prometheus.Counter
	lastReceive   prometheus.Gauge
	delay         prometheus.Histogram
}

// New returns a Consumer. It needs the queue URL and, unless a client is
// given, the consumer user's access key.
func New(o Options) (*Consumer, error) {
	if o.QueueURL == "" {
		return nil, errors.New("queue URL is required")
	}
	if o.Ingester == nil {
		return nil, errors.New("ingester is required")
	}
	region := o.Region
	if region == "" {
		var err error
		if region, err = RegionFromURL(o.QueueURL); err != nil {
			return nil, err
		}
	}
	client := o.Client
	if client == nil {
		if o.AccessKeyID == "" || o.SecretAccessKey == "" {
			return nil, errors.New("access key ID and secret access key are required")
		}
		client = sqs.New(sqs.Options{
			Region:      region,
			Credentials: credentials.NewStaticCredentialsProvider(o.AccessKeyID, o.SecretAccessKey, ""),
		})
	}
	c := &Consumer{
		queueURL: o.QueueURL,
		client:   client,
		ingester: o.Ingester,
		logger:   o.Logger,
		wait:     o.WaitTime,
		messages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mybuilds_queue_messages_total",
			Help: "Queued deliveries processed, by result.",
		}, []string{"result"}),
		receiveErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mybuilds_queue_receive_errors_total",
			Help: "Failed SQS receive calls.",
		}),
		lastReceive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mybuilds_queue_last_receive_timestamp_seconds",
			Help: "When the queue was last polled successfully.",
		}),
		delay: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "mybuilds_queue_delay_seconds",
			Help:    "Time from the Worker receiving a delivery to mybuilds processing it.",
			Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60, 300, 1800, 7200},
		}),
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	if c.wait <= 0 {
		c.wait = defaultWaitTime
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(c.messages, c.receiveErrors, c.lastReceive, c.delay)
	}
	return c, nil
}

// RegionFromURL reads the region of a queue URL such as
// https://sqs.us-west-1.amazonaws.com/123456789012/mybuilds-ingest.
func RegionFromURL(queueURL string) (string, error) {
	u, err := url.Parse(queueURL)
	if err != nil {
		return "", fmt.Errorf("queue URL: %w", err)
	}
	parts := strings.Split(u.Hostname(), ".")
	if len(parts) < 4 || parts[0] != "sqs" || parts[1] == "" {
		return "", fmt.Errorf("queue URL %q does not name a region", queueURL)
	}
	return parts[1], nil
}

// Run polls until ctx is done. Receive errors are logged and retried with
// backoff; they never stop the server.
func (c *Consumer) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		if err := c.poll(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			c.receiveErrors.Inc()
			c.logger.Error("queue receive", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = time.Second
	}
}

func (c *Consumer) poll(ctx context.Context) error {
	out, err := c.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(c.queueURL),
		MaxNumberOfMessages: maxMessages,
		WaitTimeSeconds:     int32(c.wait / time.Second),
		VisibilityTimeout:   int32(visibilityTimeout / time.Second),
	})
	if err != nil {
		return err
	}
	c.lastReceive.SetToCurrentTime()

	var done []types.DeleteMessageBatchRequestEntry
	for _, m := range out.Messages {
		if c.process(ctx, m) {
			done = append(done, types.DeleteMessageBatchRequestEntry{Id: m.MessageId, ReceiptHandle: m.ReceiptHandle})
		}
	}
	if len(done) == 0 {
		return nil
	}
	// A delete that fails leaves the message to come back once; the resend
	// is a duplicate and is dropped.
	res, err := c.client.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{QueueUrl: aws.String(c.queueURL), Entries: done})
	if err != nil {
		c.logger.Error("queue delete", "messages", len(done), "error", err)
		return nil
	}
	for _, f := range res.Failed {
		c.logger.Warn("queue delete failed", "message_id", aws.ToString(f.Id), "code", aws.ToString(f.Code))
	}
	return nil
}

// process ingests one message and reports whether it is finished with.
func (c *Consumer) process(ctx context.Context, m types.Message) bool {
	id := aws.ToString(m.MessageId)
	var env Envelope
	if err := json.Unmarshal([]byte(aws.ToString(m.Body)), &env); err != nil {
		c.messages.WithLabelValues("malformed").Inc()
		c.logger.Warn("queued message is not an envelope", "message_id", id, "error", err)
		return true
	}
	body, err := base64.StdEncoding.DecodeString(env.BodyBase64)
	if err != nil {
		c.messages.WithLabelValues("malformed").Inc()
		c.logger.Warn("queued message body is not base64", "message_id", id, "provider", env.Provider, "error", err)
		return true
	}
	if !env.ReceivedAt.IsZero() {
		c.delay.Observe(time.Since(env.ReceivedAt).Seconds())
	}

	header := http.Header{}
	for k, v := range env.Headers {
		header.Set(k, v)
	}
	res, err := c.ingester.Ingest(ctx, env.Provider, header, body)
	if err != nil {
		var ie *ingest.Error
		if errors.As(err, &ie) && ie.Status < 500 {
			// The Worker checked the signature once already, so this is a
			// changed secret or an invalid body: final either way.
			c.messages.WithLabelValues("rejected").Inc()
			c.logger.Warn("queued delivery rejected", "message_id", id, "provider", env.Provider,
				"status", ie.Status, "error", ie.Message)
			return true
		}
		c.messages.WithLabelValues("error").Inc()
		c.logger.Error("queued delivery failed", "message_id", id, "provider", env.Provider, "error", err)
		return false
	}
	c.messages.WithLabelValues(res.Outcome).Inc()
	return true
}
