// Copyright © 2022 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package source

import (
	"context"
	"fmt"

	"github.com/conduitio-labs/conduit-connector-nats-jetstream/common"
	"github.com/conduitio-labs/conduit-connector-nats-jetstream/config"
	"github.com/conduitio-labs/conduit-connector-nats-jetstream/source/jetstream"
	sdk "github.com/conduitio/conduit-connector-sdk"
	"github.com/nats-io/nats.go"
)

// Source operates source logic.
type Source struct {
	sdk.UnimplementedSource

	config   Config
	iterator *jetstream.Iterator
	errC     chan error
}

// NewSource creates new instance of the Source.
func NewSource() sdk.Source {
	return sdk.SourceWithMiddleware(&Source{}, sdk.DefaultSourceMiddleware()...)
}

// Parameters is a map of named Parameters that describe how to configure the Source.
func (s *Source) Parameters() map[string]sdk.Parameter {
	return map[string]sdk.Parameter{
		config.KeyURLs: {
			Default:     "",
			Required:    true,
			Description: "The connection URLs pointed to NATS instances.",
		},
		config.KeySubject: {
			Default:     "",
			Required:    true,
			Description: "A name of a subject from which the connector should read.",
		},
		config.KeyConnectionName: {
			Default:     "conduit-connection-<uuid>",
			Required:    false,
			Description: "Optional connection name which will come in handy when it comes to monitoring.",
		},
		config.KeyNKeyPath: {
			Default:     "",
			Required:    false,
			Description: "A path pointed to a NKey pair.",
		},
		config.KeyCredentialsFilePath: {
			Default:     "",
			Required:    false,
			Description: "A path pointed to a credentials file.",
		},
		config.KeyTLSClientCertPath: {
			Default:  "",
			Required: false,
			Description: "A path pointed to a TLS client certificate, must be present " +
				"if tls.clientPrivateKeyPath field is also present.",
		},
		config.KeyTLSClientPrivateKeyPath: {
			Default:  "",
			Required: false,
			Description: "A path pointed to a TLS client private key, must be present " +
				"if tls.clientCertPath field is also present.",
		},
		config.KeyTLSRootCACertPath: {
			Default:     "",
			Required:    false,
			Description: "A path pointed to a TLS root certificate, provide if you want to verify server’s identity.",
		},
		config.KeyMaxReconnects: {
			Default:  "5",
			Required: false,
			Description: "Sets the number of reconnect attempts " +
				"that will be tried before giving up. If negative, " +
				"then it will never give up trying to reconnect.",
		},
		config.KeyReconnectWait: {
			Default:  "5s",
			Required: false,
			Description: "Sets the time to backoff after attempting a reconnect " +
				"to a server that we were already connected to previously.",
		},
		ConfigKeyBufferSize: {
			Default:     "1024",
			Required:    false,
			Description: "A buffer size for consumed messages.",
		},
		ConfigKeyDurable: {
			Default:     "conduit-<uuid>",
			Required:    false,
			Description: "A consumer name.",
		},
		ConfigKeyDeliverSubject: {
			Default:     "<durable>.conduit",
			Required:    false,
			Description: "Specifies the JetStream consumer deliver subject.",
		},
		ConfigKeyDeliverPolicy: {
			Default:     "all",
			Required:    false,
			Description: "Defines where in the stream the connector should start receiving messages.",
		},
		ConfigKeyAckPolicy: {
			Default:     "explicit",
			Required:    false,
			Description: "Defines how messages should be acknowledged.",
		},
	}
}

// Configure parses and initializes the config.
func (s *Source) Configure(_ context.Context, cfg map[string]string) error {
	config, err := Parse(cfg)
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	s.config = config
	s.errC = make(chan error, 1)

	return nil
}

// Open opens a connection to NATS and initializes iterators.
func (s *Source) Open(_ context.Context, position sdk.Position) error {
	opts, err := common.GetConnectionOptions(s.config.Config)
	if err != nil {
		return fmt.Errorf("get connection options: %w", err)
	}

	conn, err := nats.Connect(s.config.ToURL(), opts...)
	if err != nil {
		return fmt.Errorf("connect to NATS: %w", err)
	}

	// register an error handler for async errors,
	// the Source listens to them within the Read method and propagates the error if it occurs.
	conn.SetErrorHandler(func(con *nats.Conn, sub *nats.Subscription, err error) {
		s.errC <- err
	})

	s.iterator, err = jetstream.NewIterator(jetstream.IteratorParams{
		Conn:           conn,
		BufferSize:     s.config.BufferSize,
		Durable:        s.config.Durable,
		DeliverSubject: s.config.DeliverSubject,
		Subject:        s.config.Subject,
		SDKPosition:    position,
		DeliverPolicy:  s.config.DeliverPolicy,
		AckPolicy:      s.config.AckPolicy,
	})
	if err != nil {
		return fmt.Errorf("init jetstream iterator: %w", err)
	}

	return nil
}

// Read fetches a record from an iterator.
// If there's no record will return sdk.ErrBackoffRetry.
func (s *Source) Read(ctx context.Context) (sdk.Record, error) {
	select {
	case err := <-s.errC:
		return sdk.Record{}, fmt.Errorf("got an async error: %w", err)

	default:
		if !s.iterator.HasNext() {
			return sdk.Record{}, sdk.ErrBackoffRetry
		}

		record, err := s.iterator.Next(ctx)
		if err != nil {
			return sdk.Record{}, fmt.Errorf("read next record: %w", err)
		}

		return record, nil
	}
}

// Ack acknowledges a message at the given position.
func (s *Source) Ack(_ context.Context, position sdk.Position) error {
	return s.iterator.Ack(position)
}

// Teardown closes connections, stops iterator.
func (s *Source) Teardown(context.Context) error {
	if s.iterator != nil {
		if err := s.iterator.Stop(); err != nil {
			return fmt.Errorf("stop iterator: %w", err)
		}
	}

	return nil
}
