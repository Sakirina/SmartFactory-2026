package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/configmanager"
	"competition2026/product/datatransfer/internal/connector"
	adapter "competition2026/product/datatransfer/internal/northbound/grpc"
	runtime "competition2026/product/datatransfer/internal/runtime"
	"competition2026/product/datatransfer/internal/state"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

func TestTemplateProtocolsPersistAdoptionAndRollback(t *testing.T) {
	registerIsolatedConsumers()
	for _, protocol := range []string{"mqtt_device", "modbus_tcp", "opcua"} {
		t.Run(protocol, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "datatransfer.db")
			client, manager, stop := startConsumerService(t, path)
			update := func(revision int64, sourceVersion int, password string) *dt.DeviceConfigUpdate {
				connection, err := json.Marshal(map[string]any{"url": "fixture://unreachable", "host": "127.0.0.1", "port": 1, "password": password})
				if err != nil {
					t.Fatal(err)
				}
				return &dt.DeviceConfigUpdate{UpdateId: fmt.Sprintf("fixture:%s:source-%d:application-%d", protocol, sourceVersion, revision), EntityRevision: revision, Action: dt.DeviceConfigUpdate_UPDATE_CONNECTOR, Config: &dt.DeviceConfigUpdate_ConnectorConfig{ConnectorConfig: &dt.ConnectorConfigPayload{ConnectorId: "template", Protocol: protocol, Connection: connection, Polling: []byte(`{"interval_millis":1000}`)}}}
			}
			var publicDigest string
			apply := func(u *dt.DeviceConfigUpdate) {
				t.Helper()
				response, err := client.PushDeviceConfig(t.Context(), u)
				if err != nil || !response.GetSuccess() || response.GetAppliedEntityRevision() != u.EntityRevision {
					t.Fatalf("apply revision %d: response=%v error=%v", u.EntityRevision, response, err)
				}
				observed, err := client.GetConnectorConfiguration(t.Context(), &dt.ConnectorConfigurationRequest{ConnectorId: "template", ExpectedConfiguration: u.GetConnectorConfig()})
				if err != nil || !observed.GetFound() || !observed.GetMatchesExpected() || observed.GetAppliedEntityRevision() != u.EntityRevision || observed.GetAppliedUpdateId() != u.UpdateId || observed.GetPublicConfigurationSha256() == "" {
					t.Fatalf("observe adopted revision %d: state=%v error=%v", u.EntityRevision, observed, err)
				}
				if publicDigest == "" {
					publicDigest = observed.PublicConfigurationSha256
				} else if publicDigest != observed.PublicConfigurationSha256 {
					t.Fatal("a secret-only adoption changed the public configuration digest")
				}
				actual, ok := manager.ConnectorConfig("template")
				if !ok || actual.Protocol != protocol || actual.Connection.Password == "" {
					t.Fatal("isolated adapter did not consume the protocol and private credential")
				}
			}
			first := update(1, 1, "private-version-one")
			second := update(2, 2, "private-version-two")
			rollback := update(3, 1, "private-version-one")
			apply(first)
			apply(second)
			apply(rollback)
			stop()

			client, manager, stop = startConsumerService(t, path)
			defer stop()
			apply(rollback)
			actual, _ := manager.ConnectorConfig("template")
			if actual.Connection.Password != "private-version-one" {
				t.Fatal("journal restart did not restore the rolled-back fixed secret")
			}
			changed := proto.Clone(rollback).(*dt.DeviceConfigUpdate)
			changed.GetConnectorConfig().Connection = []byte(`{"password":"different-private-value"}`)
			response, err := client.PushDeviceConfig(t.Context(), changed)
			if err != nil || response.GetSuccess() {
				t.Fatalf("conflicting reuse of a persistent application identity: response=%v error=%v", response, err)
			}
			t.Log("production gRPC and ConfigManager applied source 1, secret-only source 2, and source 1 rollback at successive application revisions; journal restart preserved identity and complete payload")
		})
	}
}

func startConsumerService(t *testing.T, path string) (dt.DataTransferServiceClient, *connector.Manager, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	rt := runtime.New(config.Defaults())
	journal, err := state.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := connector.NewManager(nil, rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	rt.AttachConnectorManager(manager)
	cm := configmanager.New(manager, nil)
	cm.SetGlobalApplier(rt)
	rt.AttachConfigManager(cm)
	if err := cm.AttachJournal(ctx, journal); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- manager.Start(ctx) }()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	adapter.Register(server, rt)
	go server.Serve(listener)
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		connection.Close()
		server.Stop()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("isolated consumer manager did not stop")
		}
		rt.Close()
		if err := journal.Close(); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(stop)
	return dt.NewDataTransferServiceClient(connection), manager, stop
}
