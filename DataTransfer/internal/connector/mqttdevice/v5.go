package mqttdevice

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"competition2026/product/datatransfer/internal/connector"
	"competition2026/product/datatransfer/internal/security"
	"github.com/eclipse/paho.golang/autopaho"
	paho5 "github.com/eclipse/paho.golang/paho"
)

func (c *Connector) connectProtocol(ctx context.Context) error {
	c.mu.RLock()
	cfg := c.cfg
	c.mu.RUnlock()
	if cfg.Connection.MQTTVersion != "5.0" {
		return c.connect()
	}
	target, err := url.Parse(strings.Replace(cfg.Connection.URL, "tcp://", "mqtt://", 1))
	if err != nil {
		return err
	}
	tlsCfg, err := security.TLSConfig(cfg.Connection.TLS)
	if err != nil {
		return err
	}
	if cfg.Connection.TLS.Enabled && target.Scheme == "mqtt" {
		target.Scheme = "tls"
	}
	ready := make(chan error, 1)
	cm, err := autopaho.NewConnection(ctx, autopaho.ClientConfig{
		ServerUrls: []*url.URL{target}, TlsCfg: tlsCfg, KeepAlive: 15,
		CleanStartOnInitialConnection: true, SessionExpiryInterval: 86400,
		ConnectTimeout:   time.Duration(timeoutMillis(cfg.Connection.TimeoutMillis)) * time.Millisecond,
		ReconnectBackoff: func(int) time.Duration { return time.Second },
		ConnectUsername:  cfg.Connection.Username, ConnectPassword: []byte(cfg.Connection.Password),
		OnConnectionUp: func(cm *autopaho.ConnectionManager, _ *paho5.Connack) {
			go func() {
				call, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				sub := &paho5.Subscribe{}
				for _, r := range topicRoutes(cfg) {
					sub.Subscriptions = append(sub.Subscriptions, paho5.SubscribeOptions{Topic: subscriptionFilter(r.template), QoS: 1})
				}
				ack, e := cm.Subscribe(call, sub)
				if e == nil {
					for _, code := range ack.Reasons {
						if code >= 128 {
							e = errors.New("MQTT 5 broker refused subscription")
						}
					}
				}
				if e != nil {
					c.setState(connector.StateError, e.Error())
				} else {
					c.setState(connector.StateRunning, "")
				}
				select {
				case ready <- e:
				default:
				}
			}()
		},
		OnConnectionDown: func() bool { c.setState(connector.StateError, "broker disconnected"); return true },
		ClientConfig: paho5.ClientConfig{ClientID: clientID(cfg), PacketTimeout: 5 * time.Second,
			OnPublishReceived: []func(paho5.PublishReceived) (bool, error){func(r paho5.PublishReceived) (bool, error) {
				c.routePayload(r.Packet.Topic, r.Packet.Payload)
				return true, nil
			}}},
	})
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.v5 = cm
	c.mu.Unlock()
	select {
	case err := <-ready:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(10 * time.Second):
		return errors.New("MQTT 5 subscription timed out")
	}
}
