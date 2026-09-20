package acceptance

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	adapter "competition2026/product/datatransfer/internal/northbound/grpc"
	dtruntime "competition2026/product/datatransfer/internal/runtime"
	"competition2026/product/datatransfer/internal/state"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

const eventTime int64 = 1900000000000

func event(id int) *dt.DeviceMessage {
	return &dt.DeviceMessage{MessageId: fmt.Sprintf("acceptance:%08d", id), Timestamp: eventTime + int64(id), SourceSequence: uint64(id), Direction: dt.Direction_UPSTREAM, Type: dt.MessageType_EVENT, Device: &dt.DeviceIdentity{DeviceId: "counter"}, Payload: &dt.DeviceMessage_Event{Event: &dt.EventPayload{EventType: "pulse", Data: map[string]string{"pulse": "1"}}}}
}

func TestDurableProcessFixture(t *testing.T) {
	if os.Getenv("SF_DURABLE_CHILD") != "1" {
		t.Skip("subprocess fixture")
	}
	ctx := context.Background()
	directory := os.Getenv("SF_DURABLE_DIRECTORY")
	total, _ := strconv.Atoi(os.Getenv("SF_ACCEPTANCE_COUNT"))
	journal, err := state.Open(ctx, filepath.Join(directory, "sender.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	rt := dtruntime.New(config.Defaults())
	rt.AttachCommandJournal(journal)
	defer rt.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	adapter.Register(server, rt)
	go server.Serve(listener)
	defer server.Stop()
	web, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(web, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		message := event(0)
		message.MessageId = "failure:write"
		if err := rt.Publish(message); err != nil {
			http.Error(w, "storage unavailable", 503)
			return
		}
		w.WriteHeader(200)
	}))
	defer web.Close()
	fmt.Printf("READY %s %s\n", listener.Addr(), web.Addr())
	if _, err = os.Stat(filepath.Join(directory, "generated")); os.IsNotExist(err) {
		order := rand.New(rand.NewSource(731)).Perm(total)
		for index, id := range order {
			message := event(id + 1)
			if err = rt.Publish(message); err != nil {
				t.Fatal(err)
			}
			if index%11 == 0 {
				if err = rt.Publish(message); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err = os.WriteFile(filepath.Join(directory, "generated"), []byte("committed\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	select {}
}

func TestDurableMessagesFaultInjection(t *testing.T) {
	text := os.Getenv("SF_ACCEPTANCE_COUNT")
	if text == "" {
		t.Skip("set SF_ACCEPTANCE_COUNT=100000 for A06")
	}
	total, err := strconv.Atoi(text)
	if err != nil || total < 1000 {
		t.Fatal("count must be >= 1000")
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	directory := t.TempDir()
	receiver, err := sql.Open("sqlite", filepath.Join(directory, "receiver.db"))
	if err != nil {
		t.Fatal(err)
	}
	receiver.SetMaxOpenConns(1)
	defer receiver.Close()
	if _, err = receiver.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; CREATE TABLE effects(id TEXT PRIMARY KEY,digest TEXT NOT NULL,value INTEGER NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	var child *exec.Cmd
	var conn *grpc.ClientConn
	var client dt.DataTransferServiceClient
	var web string
	stop := func() {
		if conn != nil {
			conn.Close()
			conn = nil
		}
		if child != nil {
			_ = child.Process.Kill()
			_ = child.Wait()
			child = nil
		}
	}
	defer stop()
	start := func() {
		child = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDurableProcessFixture$", "-test.timeout=15m")
		child.Env = append(os.Environ(), "SF_DURABLE_CHILD=1", "SF_DURABLE_DIRECTORY="+directory)
		stdout, e := child.StdoutPipe()
		if e != nil {
			t.Fatal(e)
		}
		var errors bytes.Buffer
		child.Stderr = &errors
		if e = child.Start(); e != nil {
			t.Fatal(e)
		}
		reader := bufio.NewScanner(stdout)
		if !reader.Scan() {
			t.Fatalf("child startup failed: %s", errors.String())
		}
		parts := strings.Fields(reader.Text())
		if len(parts) != 3 || parts[0] != "READY" {
			t.Fatalf("child readiness: %v", parts)
		}
		go io.Copy(io.Discard, stdout)
		web = parts[2]
		conn, e = grpc.NewClient(parts[1], grpc.WithTransportCredentials(insecure.NewCredentials()))
		if e != nil {
			t.Fatal(e)
		}
		client = dt.NewDataTransferServiceClient(conn)
	}
	start()
	lost := map[string]bool{}
	seen, deliveries, duplicates, restarts := 0, 0, 0, 0
	nextRestart := total / 4
	for seen < total {
		if err := ctx.Err(); err != nil {
			t.Fatalf("processed %d/%d: %v", seen, total, err)
		}
		batch, e := client.PullPendingMessages(ctx, &dt.PendingRequest{Limit: 1000})
		if e != nil {
			t.Fatal(e)
		}
		if len(batch.Messages) == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		tx, e := receiver.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		acks := []*dt.MessageAcknowledgement{}
		for _, msg := range batch.Messages {
			deliveries++
			raw, e := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
			if e != nil {
				t.Fatal(e)
			}
			digest := sha256.Sum256(raw)
			hash := hex.EncodeToString(digest[:])
			result, e := tx.ExecContext(ctx, `INSERT OR IGNORE INTO effects(id,digest,value) VALUES(?,?,1)`, msg.MessageId, hash)
			if e != nil {
				t.Fatal(e)
			}
			n, e := result.RowsAffected()
			if e != nil {
				t.Fatal(e)
			}
			if n == 1 {
				seen++
			} else {
				duplicates++
				var prior string
				if e = tx.QueryRowContext(ctx, `SELECT digest FROM effects WHERE id=?`, msg.MessageId).Scan(&prior); e != nil || prior != hash {
					t.Fatal("conflicting duplicate")
				}
			}
			if msg.SourceSequence%7 == 0 && !lost[msg.MessageId] {
				lost[msg.MessageId] = true
				continue
			}
			acks = append(acks, &dt.MessageAcknowledgement{MessageId: msg.MessageId, PayloadSha256: digest[:], ReceiverId: "business-receiver"})
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		for _, ack := range acks {
			if _, e = client.AcknowledgeMessage(ctx, ack); e != nil {
				t.Fatal(e)
			}
		}
		if seen >= nextRestart && restarts < 3 {
			stop()
			restarts++
			nextRestart = total * (restarts + 1) / 4
			start()
			t.Logf("receiver committed %d/%d; restarted sender %d", seen, total, restarts)
		}
	}
	// The final lost acknowledgements are retried after their receiver commit.
	for {
		batch, e := client.PullPendingMessages(ctx, &dt.PendingRequest{Limit: 1000})
		if e != nil {
			t.Fatal(e)
		}
		if len(batch.Messages) == 0 {
			break
		}
		for _, msg := range batch.Messages {
			var exists int
			if e = receiver.QueryRowContext(ctx, `SELECT COUNT(*) FROM effects WHERE id=?`, msg.MessageId).Scan(&exists); e != nil || exists != 1 {
				t.Fatal("uncommitted acknowledgement")
			}
			raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
			hash := sha256.Sum256(raw)
			if _, e = client.AcknowledgeMessage(ctx, &dt.MessageAcknowledgement{MessageId: msg.MessageId, PayloadSha256: hash[:], ReceiverId: "business-receiver"}); e != nil {
				t.Fatal(e)
			}
			duplicates++
			deliveries++
		}
	}
	sender, e := sql.Open("sqlite", filepath.Join(directory, "sender.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer sender.Close()
	_, _ = sender.Exec(`PRAGMA busy_timeout=5000`)
	if _, e = sender.Exec(`CREATE TRIGGER reject_write BEFORE INSERT ON durable_outbox WHEN new.message_id='failure:write' BEGIN SELECT RAISE(ABORT,'injected disk write failure'); END;`); e != nil {
		t.Fatal(e)
	}
	response, e := http.Post("http://"+web, "application/json", nil)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatalf("database write failure acknowledged: %d", response.StatusCode)
	}
	var effects, sum, confirmed, pending, failed int
	if e = receiver.QueryRow(`SELECT COUNT(*),SUM(value) FROM effects`).Scan(&effects, &sum); e != nil {
		t.Fatal(e)
	}
	if e = sender.QueryRow(`SELECT COUNT(*) FROM durable_outbox WHERE acknowledged=1`).Scan(&confirmed); e != nil {
		t.Fatal(e)
	}
	if e = sender.QueryRow(`SELECT COUNT(*) FROM durable_outbox WHERE acknowledged=0`).Scan(&pending); e != nil {
		t.Fatal(e)
	}
	if e = sender.QueryRow(`SELECT COUNT(*) FROM durable_outbox WHERE message_id='failure:write'`).Scan(&failed); e != nil {
		t.Fatal(e)
	}
	if effects != total || sum != total || confirmed != total || pending != 0 || failed != 0 {
		t.Fatalf("effects=%d sum=%d confirmed=%d pending=%d failed=%d", effects, sum, confirmed, pending, failed)
	}
	report := map[string]any{"case": "A06", "status": "passed", "environment": runtime.GOOS + "/" + runtime.GOARCH, "messages": total, "input_duplicate_every": 11, "shuffled_input_seed": 731, "lost_acknowledgements": len(lost), "delivery_attempts": deliveries, "deduplicated_deliveries": duplicates, "sender_process_restarts": restarts, "confirmed_events": confirmed, "missing_confirmed_events": confirmed - effects, "duplicate_business_effects": sum - effects, "successful_write_failure_acknowledgements": 0, "duration_seconds": time.Since(started).Seconds()}
	report["full_acceptance_count"] = total >= 100000
	if total < 100000 {
		report["status"] = "regression_passed"
	}
	raw, _ := json.MarshalIndent(report, "", "  ")
	t.Log(string(raw))
	if path := os.Getenv("SF_A06_EVIDENCE"); path != "" {
		if e = os.WriteFile(path, append(raw, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
}
