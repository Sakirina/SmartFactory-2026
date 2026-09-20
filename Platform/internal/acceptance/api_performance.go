package acceptance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"competition2026/product/platform/internal/app"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type APIOptions struct {
	Directory, DSN, StaticDir, Listen string
	Duration                          time.Duration
	QueriesPerClient                  int
	Profile                           bool
}
type apiMeasurements struct {
	sync.Mutex
	Stream, Raw, Day             []float64
	StreamEvents, HistoryQueries int
	MaxSeriesPoints              int
}

func RunAPI(parent context.Context, o APIOptions) (map[string]any, error) {
	if o.Duration < 30*time.Second || o.QueriesPerClient < 1 {
		return nil, fmt.Errorf("duration >= 30s and queries >= 1 required")
	}
	if err := os.MkdirAll(o.Directory, 0700); err != nil {
		return nil, err
	}
	if o.DSN == "" {
		o.DSN = ":memory:"
	}
	if o.Listen == "" {
		listener, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			return nil, e
		}
		o.Listen = listener.Addr().String()
		listener.Close()
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	password, serviceToken := identity.ID()+identity.ID(), identity.ID()+identity.ID()
	a, e := app.Open(ctx, app.Options{Mode: "cloud", NodeID: "api-benchmark", Address: o.Listen, DSN: o.DSN, KeyFile: filepath.Join(o.Directory, "master.key"), BootstrapPassword: password, ServiceToken: serviceToken, StaticDir: o.StaticDir})
	if e != nil {
		return nil, e
	}
	defer a.Store.Close()
	var existing int
	if e = a.Store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM observations").Scan(&existing); e != nil {
		return nil, e
	}
	if existing != 0 {
		return nil, fmt.Errorf("benchmark requires an empty observation database")
	}
	historyEnd := time.Now().Add(-time.Second).UnixMilli()
	devices, seeded, e := seedAPI(ctx, a.Store, historyEnd)
	if e != nil {
		return nil, e
	}
	archiveStats := store.ArchiveStats{}
	for {
		stats, err := a.Store.ArchiveObservations(ctx)
		if err != nil {
			return nil, err
		}
		archiveStats.Blocks += stats.Blocks
		archiveStats.Points += stats.Points
		archiveStats.PlainBytes += stats.PlainBytes
		archiveStats.CompressedBytes += stats.CompressedBytes
		if stats.Points == 0 {
			break
		}
	}
	token, _, e := a.Server.Identity.Login(ctx, "admin", password, "", false, "acceptance")
	if e != nil {
		return nil, e
	}
	base := "http://" + o.Listen
	private, _ := json.Marshal(map[string]string{"base_url": base, "login": "admin", "password": password})
	if e = os.WriteFile(filepath.Join(o.Directory, "browser-connection.json"), append(private, '\n'), 0600); e != nil {
		return nil, e
	}
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(ctx) }()
	defer func() { cancel(); <-runDone }()
	client := &http.Client{Timeout: 30 * time.Second}
	for until := time.Now().Add(10 * time.Second); ; {
		response, e := client.Get(base + "/health")
		if e == nil {
			response.Body.Close()
			break
		}
		if time.Now().After(until) {
			return nil, e
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Printf("API fixture ready at %s: %d raw samples over 30 days and %d day aggregates over three years\n", base, seeded["raw"], seeded["day"])
	measurements := new(apiMeasurements)
	streamCtx, stopStreams := context.WithCancel(ctx)
	defer stopStreams()
	streamClient := &http.Client{}
	ready := make(chan struct{}, 20)
	failures := make(chan error, 64)
	var streams sync.WaitGroup
	for _, device := range devices {
		streams.Add(1)
		go func(device string) {
			defer streams.Done()
			request, _ := http.NewRequestWithContext(streamCtx, "GET", base+"/api/sf/v1/events?device_ids="+device+"&keys=value&window_ms=5000&limit=200", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response, e := streamClient.Do(request)
			if e != nil {
				failures <- e
				return
			}
			defer response.Body.Close()
			if response.StatusCode != 200 {
				failures <- fmt.Errorf("stream status %d", response.StatusCode)
				return
			}
			scanner := bufio.NewScanner(response.Body)
			scanner.Buffer(make([]byte, 4096), 2<<20)
			seen := map[string]bool{}
			first := true
			for scanner.Scan() {
				line := scanner.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var result model.DataResult
				if e := store.DecodeJSON([]byte(line[6:]), &result); e != nil {
					failures <- e
					return
				}
				arrived := time.Now().UnixMilli()
				if first {
					first = false
					ready <- struct{}{}
				}
				measurements.Lock()
				measurements.StreamEvents++
				for _, p := range result.Points {
					if strings.HasPrefix(p.ID, "live:") && !seen[p.ID] {
						seen[p.ID] = true
						measurements.Stream = append(measurements.Stream, float64(arrived-p.ObservedMS))
					}
				}
				measurements.Unlock()
			}
			if streamCtx.Err() == nil {
				failures <- fmt.Errorf("stream ended: %v", scanner.Err())
			}
		}(device)
	}
	for i := 0; i < 20; i++ {
		select {
		case <-ready:
		case e := <-failures:
			return nil, e
		case <-time.After(30 * time.Second):
			return nil, fmt.Errorf("twenty streams did not become ready")
		}
	}
	started := time.Now()
	if o.Profile {
		profile, err := os.Create(filepath.Join(o.Directory, "cpu.pprof"))
		if err != nil {
			return nil, err
		}
		if err = pprof.StartCPUProfile(profile); err != nil {
			profile.Close()
			return nil, err
		}
		defer func() { pprof.StopCPUProfile(); profile.Close() }()
	}
	producerCtx, stopProducer := context.WithCancel(ctx)
	defer stopProducer()
	var ingested atomic.Int64
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for sequence := 1; ; sequence++ {
			due := started.Add(time.Duration(sequence) * 40 * time.Millisecond)
			if due.After(started.Add(o.Duration)) {
				return
			}
			if wait := time.Until(due); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-producerCtx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			if producerCtx.Err() != nil {
				return
			}
			// Keep the source schedule after a delayed request. Measured display
			// latency includes queueing instead of silently dropping offered ticks.
			at := due.UnixMilli()
			batch := store.IngestBatch{MessageID: fmt.Sprintf("live:%d", sequence), SourceID: "api-benchmark", Points: make([]model.Observation, 0, 20)}
			for i, id := range devices {
				batch.Points = append(batch.Points, model.Observation{DeviceID: id, Key: "value", Value: sequence + i, ObservedMS: at, Quality: "GOOD", TimeSource: "device", EntityRevision: 1, AssetVersion: 1})
			}
			raw, _ := json.Marshal(batch)
			request, _ := http.NewRequestWithContext(producerCtx, "POST", base+"/api/sf/v1/ingest", bytes.NewReader(raw))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+serviceToken)
			response, e := client.Do(request)
			if e != nil {
				if producerCtx.Err() == nil {
					failures <- e
				}
				return
			}
			var result store.IngestResult
			e = json.NewDecoder(response.Body).Decode(&result)
			response.Body.Close()
			if e != nil || response.StatusCode != 200 || !result.Committed || result.Count != 20 {
				failures <- fmt.Errorf("ingest status=%d count=%d error=%v", response.StatusCode, result.Count, e)
				return
			}
			ingested.Add(int64(result.Count))
		}
	}()
	var histories sync.WaitGroup
	for worker := 0; worker < 10; worker++ {
		histories.Add(1)
		go func(worker int) {
			defer histories.Done()
			for i := 0; i < o.QueriesPerClient; i++ {
				grain := "raw"
				from := historyEnd - int64(30*24*time.Hour/time.Millisecond) + 1
				if (worker+i)%2 == 1 {
					grain = "day"
					from = historyEnd - int64(1095*24*time.Hour/time.Millisecond)
				}
				url := fmt.Sprintf("%s/api/sf/v1/data?device_ids=%s&keys=value&from_ms=%d&to_ms=%d&resolution=%s&limit=40000", base, strings.Join(devices, ","), from, historyEnd, grain)
				request, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
				request.Header.Set("Authorization", "Bearer "+token)
				at := time.Now()
				response, e := client.Do(request)
				if e != nil {
					failures <- e
					return
				}
				var result model.DataResult
				e = store.DecodeJSONReader(response.Body, &result)
				response.Body.Close()
				elapsed := float64(time.Since(at)) / float64(time.Millisecond)
				fmt.Printf("history worker=%d grain=%s elapsed_ms=%.1f points=%d\n", worker, grain, elapsed, len(result.Points))
				if e != nil || response.StatusCode != 200 {
					failures <- fmt.Errorf("history status %d: %v", response.StatusCode, e)
					return
				}
				perSeries := map[string]int{}
				for _, point := range result.Points {
					perSeries[point.DeviceID+"/"+point.Key]++
				}
				measurements.Lock()
				measurements.HistoryQueries++
				if grain == "raw" {
					measurements.Raw = append(measurements.Raw, elapsed)
				} else {
					measurements.Day = append(measurements.Day, elapsed)
				}
				for _, count := range perSeries {
					measurements.MaxSeriesPoints = max(measurements.MaxSeriesPoints, count)
				}
				measurements.Unlock()
				if len(perSeries) != 20 || result.Truncated {
					failures <- fmt.Errorf("history returned %d series, truncated=%v", len(perSeries), result.Truncated)
					return
				}
				wait := o.Duration/time.Duration(o.QueriesPerClient) - time.Since(at)
				if i+1 < o.QueriesPerClient && wait > 0 {
					select {
					case <-ctx.Done():
						return
					case <-time.After(wait):
					}
				}
			}
		}(worker)
	}
	select {
	case <-time.After(o.Duration):
	case e := <-failures:
		return nil, e
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	stopProducer()
	<-producerDone
	histories.Wait()
	select {
	case e := <-failures:
		return nil, e
	case <-time.After(1500 * time.Millisecond):
	}
	stopStreams()
	streams.Wait()
	measurements.Lock()
	defer measurements.Unlock()
	var count int64
	if e = a.Store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM observations WHERE observed_ms>=$1 AND definition_id=''", started.UnixMilli()).Scan(&count); e != nil {
		return nil, e
	}
	history := append(append([]float64{}, measurements.Raw...), measurements.Day...)
	report := map[string]any{"archived_history": archiveStats, "acceptance": "A15", "environment": runtime.GOOS + "/" + runtime.GOARCH, "go_version": runtime.Version(), "database_driver": a.Store.Driver, "started_at": started.UTC().Format(time.RFC3339Nano), "duration_seconds": o.Duration.Seconds(), "concurrent_streams": 20, "concurrent_history_clients": 10, "history_requests": measurements.HistoryQueries, "raw_seed_points": seeded["raw"], "raw_span_days": 30, "day_seed_points": seeded["day"], "rollup_span_days": 1095, "max_series": 20, "maximum_returned_points_per_series": measurements.MaxSeriesPoints, "ingested_live_points": ingested.Load(), "stored_live_points": count, "offered_points_per_second": 500, "committed_points_per_second": float64(ingested.Load()) / o.Duration.Seconds(), "stream_unique_observations": len(measurements.Stream), "stream_events": measurements.StreamEvents, "stream_p95_ms": percentile(measurements.Stream, .95), "history_p95_ms": percentile(history, .95), "raw_history_p95_ms": percentile(measurements.Raw, .95), "day_history_p95_ms": percentile(measurements.Day, .95), "stream_pass": percentile(measurements.Stream, .95) <= 2000 && len(measurements.Stream) > 0, "history_pass": percentile(history, .95) <= 2000 && measurements.HistoryQueries == 10*o.QueriesPerClient, "counts_match": count == ingested.Load(), "measurement": "actual authenticated HTTP API; SSE receive latency measured for every first-seen live observation; browser paint is verified separately"}
	report["target_load_pass"] = float64(ingested.Load())/o.Duration.Seconds() >= 495
	report["api_criteria_pass"] = report["target_load_pass"].(bool) && report["stream_pass"].(bool) && report["history_pass"].(bool) && report["counts_match"].(bool)
	return report, nil
}
func percentile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]float64{}, values...)
	sort.Float64s(copyValues)
	return copyValues[min(len(copyValues)-1, int(float64(len(copyValues))*q))]
}
func seedAPI(ctx context.Context, s *store.Store, end int64) ([]string, map[string]int, error) {
	devices := []string{}
	if _, err := s.Put(ctx, "entity", "benchmark-factory", 0, model.Entity{ID: "benchmark-factory", Name: "页面延迟验收", Kind: "asset", Status: "active", Version: 1}); err != nil {
		return nil, nil, err
	}
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("series-%02d", i)
		devices = append(devices, id)
		if _, e := s.Put(ctx, "entity", id, 0, model.Entity{ID: id, Name: id, Kind: "device", ParentID: "benchmark-factory", Status: "approved", EdgeID: "api-benchmark", Version: 1, SamplingMS: 40}); e != nil {
			return nil, nil, e
		}
		dashboard := model.Dashboard{ID: id, Title: "实时页面 " + id, GroupID: "benchmark-factory", Version: 1, DeviceIDs: []string{id}, Keys: []string{"value"}, WindowMS: 5000, RefreshMS: -1,
			Metrics: []model.DashboardMetric{{DeviceID: id, Key: "value", Label: "实时测点"}}}
		if _, err := s.Put(ctx, "dashboard", id, 0, dashboard); err != nil {
			return nil, nil, err
		}
	}
	e := s.Write(ctx, func(tx *store.Tx) error {
		rows := [][]any{}
		flush := func() error {
			err := insertFixtureRows(ctx, tx, "observations", "id,observed_ms,message_id,device_id,key,received_ms,revision,quality,definition_id,data", rows)
			rows = nil
			return err
		}
		for i, id := range devices {
			for point := 0; point < 2000; point++ {
				at := end - int64(30*24*time.Hour/time.Millisecond) + 1 + int64(point)*int64(30*24*time.Hour/time.Millisecond-2)/1999
				p := model.Observation{ID: fmt.Sprintf("seed:%s:%d", id, point), MessageID: fmt.Sprintf("seed:%s:%d", id, point), DeviceID: id, Key: "value", SourceID: "api-benchmark", Value: (i*17 + point) % 100, ObservedMS: at, ReceivedMS: at, Quality: "GOOD", TimeSource: "deterministic_fixture", Revision: 1, EntityRevision: 1, AssetVersion: 1}
				if e := tx.EnsureDay(at); e != nil {
					return e
				}
				raw, e := json.Marshal(p)
				if e != nil {
					return e
				}
				rows = append(rows, []any{p.ID, at, p.MessageID, id, p.Key, at, 1, "GOOD", "", string(raw)})
				if len(rows) == 500 {
					if e := flush(); e != nil {
						return e
					}
				}
			}
			fmt.Printf("Prepared raw series %d/20\n", i+1)
		}
		return flush()
	})
	if e != nil {
		return nil, nil, e
	}
	e = s.Write(ctx, func(tx *store.Tx) error {
		rows := [][]any{}
		flush := func() error {
			err := insertFixtureRows(ctx, tx, "rollups", "device_id,key,granularity,bucket_ms,data", rows)
			rows = nil
			return err
		}
		for _, id := range devices {
			for day := 0; day < 1095; day++ {
				at := end/int64(24*time.Hour/time.Millisecond)*int64(24*time.Hour/time.Millisecond) - int64(day)*int64(24*time.Hour/time.Millisecond)
				average := float64(day % 100)
				raw, _ := json.Marshal(map[string]any{"aggregate": store.Aggregate{Count: 86400, Sum: json.Number(fmt.Sprint(int64(day%100) * 86400)), Min: json.Number(fmt.Sprint(day % 100)), Max: json.Number(fmt.Sprint(day % 100)), Average: &average, Complete: "complete"}, "unit": "count"})
				rows = append(rows, []any{id, "value", "day", at, string(raw)})
				if len(rows) == 500 {
					if e := flush(); e != nil {
						return e
					}
				}
			}
		}
		return flush()
	})
	if e == nil {
		// This fixture supplies precomputed historical aggregates. Record its
		// completed archival cursor so the measurement starts in steady state.
		_, e = s.Put(ctx, "retention_cursor", "rollup", 0, map[string]any{"next_ms": time.UnixMilli(end).Truncate(time.Hour).UnixMilli(), "fixture": "precomputed A15 history"})
	}
	return devices, map[string]int{"raw": 40000, "day": 21900}, e
}

func insertFixtureRows(ctx context.Context, tx *store.Tx, table, columns string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	var query strings.Builder
	query.WriteString("INSERT INTO " + table + "(" + columns + ") VALUES ")
	args := []any{}
	for i, row := range rows {
		if i > 0 {
			query.WriteByte(',')
		}
		query.WriteByte('(')
		for j, value := range row {
			if j > 0 {
				query.WriteByte(',')
			}
			args = append(args, value)
			fmt.Fprintf(&query, "$%d", len(args))
		}
		query.WriteByte(')')
	}
	_, err := tx.ExecContext(ctx, query.String(), args...)
	return err
}
