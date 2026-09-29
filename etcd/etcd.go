// Package etcd provides an ETCD-backed configuration Source for
// github.com/gocrud/kernel. The returned Source implements both kernel.Source
// (pull, via etcd Get) and kernel.WatchSource (push, via etcd watch), so it
// works with static Config bindings and with the WithReloadable option alike.
//
// Keys under the configured prefix are mapped to dotted config keys: with
// Key("/myapp/app") and Section("app"), the etcd key /myapp/app/port becomes
// the config key app.port. Values are strings; the framework's binding layer
// converts them to the target field types.
package etcd

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// Source is an ETCD-backed configuration source.
type Source struct {
	client  *clientv3.Client
	key     string
	prefix  bool
	section string
	timeout time.Duration
}

type options struct {
	endpoints   []string
	key         string
	prefix      bool
	section     string
	dialTimeout time.Duration
	timeout     time.Duration
}

// Option configures Source construction.
type Option func(*options)

// Endpoints sets the etcd endpoints. Defaults to 127.0.0.1:2379.
func Endpoints(e ...string) Option { return func(o *options) { o.endpoints = e } }

// Key sets the key or key prefix to read. Required.
func Key(k string) Option { return func(o *options) { o.key = k } }

// Prefix reads Key as a prefix (WithPrefix). Defaults to false (exact key).
func Prefix(p bool) Option { return func(o *options) { o.prefix = p } }

// Section prefixes mapped keys with the given dotted section, so that
// Key("/myapp/app") + Section("app") maps /myapp/app/port to app.port.
func Section(s string) Option { return func(o *options) { o.section = s } }

// DialTimeout sets the connection dial timeout.
func DialTimeout(d time.Duration) Option { return func(o *options) { o.dialTimeout = d } }

// Timeout sets the Get timeout used by Load.
func Timeout(d time.Duration) Option { return func(o *options) { o.timeout = d } }

// New connects to etcd and returns a Source.
func New(opts ...Option) (*Source, error) {
	o := options{
		endpoints:   []string{"127.0.0.1:2379"},
		dialTimeout: 5 * time.Second,
		timeout:     5 * time.Second,
	}
	for _, f := range opts {
		f(&o)
	}
	if o.key == "" {
		return nil, fmt.Errorf("etcd: Key is required")
	}
	c, err := clientv3.New(clientv3.Config{Endpoints: o.endpoints, DialTimeout: o.dialTimeout})
	if err != nil {
		return nil, fmt.Errorf("etcd: dial: %w", err)
	}
	return &Source{
		client:  c,
		key:     o.key,
		prefix:  o.prefix,
		section: o.section,
		timeout: o.timeout,
	}, nil
}

// Name identifies the source in diagnostics and errors.
func (s *Source) Name() string { return "etcd:" + s.key }

// Load pulls the current values with etcd Get and maps them to config keys.
func (s *Source) Load() (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	var (
		resp *clientv3.GetResponse
		err  error
	)
	if s.prefix {
		resp, err = s.client.Get(ctx, s.key, clientv3.WithPrefix())
	} else {
		resp, err = s.client.Get(ctx, s.key)
	}
	if err != nil {
		return nil, fmt.Errorf("etcd: get: %w", err)
	}

	out := make(map[string]any, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		out[s.mapKey(string(kv.Key))] = string(kv.Value)
	}
	return out, nil
}

// Watch starts pushing change notifications until ctx is done.
func (s *Source) Watch(ctx context.Context, onChange func(error)) error {
	wch := s.client.Watch(ctx, s.key, clientv3.WithPrefix())
	go func() {
		for resp := range wch {
			if err := resp.Err(); err != nil {
				onChange(err)
				return
			}
			if resp.Canceled {
				onChange(ctx.Err())
				return
			}
			onChange(nil)
		}
		onChange(context.Canceled)
	}()
	return nil
}

// Close releases the underlying etcd client.
func (s *Source) Close() error { return s.client.Close() }

// mapKey converts an etcd key into a dotted config key: the configured prefix
// is stripped, path separators become dots, and Section (if set) is prepended.
func (s *Source) mapKey(key string) string {
	rel := strings.TrimPrefix(key, s.key)
	rel = strings.Trim(rel, "/")
	if rel == "" {
		rel = path.Base(strings.Trim(s.key, "/"))
	}
	k := strings.ReplaceAll(rel, "/", ".")
	if s.section != "" {
		k = s.section + "." + k
	}
	return k
}
