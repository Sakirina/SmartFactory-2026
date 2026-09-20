package acceptance

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

//go:embed opcua_capacity.py
var opcuaPeerScript []byte

// The independent peer honors monitored-item queue size. Its stdin response
// confirms a simulator write; samples are still received over actual OPC-UA.
func startOPCUAPeer(ctx context.Context, python, directory string, port int) (int, func(uint32) (int64, error), func(), error) {
	path := filepath.Join(directory, "opcua_capacity.py")
	if err := os.WriteFile(path, opcuaPeerScript, 0600); err != nil {
		return 0, nil, nil, err
	}
	log, err := os.Create(filepath.Join(directory, "opcua-peer.log"))
	if err != nil {
		return 0, nil, nil, err
	}
	peerCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(peerCtx, python, "-u", path, "--port", strconv.Itoa(port))
	cmd.Stderr = log
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		log.Close()
		return 0, nil, nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		in.Close()
		log.Close()
		return 0, nil, nil, err
	}
	if err = cmd.Start(); err != nil {
		cancel()
		in.Close()
		log.Close()
		return 0, nil, nil, err
	}
	cleanup := func() { in.Close(); cancel(); _ = cmd.Wait(); log.Close() }
	decoder, encoder := json.NewDecoder(out), json.NewEncoder(in)
	var ready struct {
		Namespace int `json:"namespace"`
	}
	errCh := make(chan error, 1)
	go func() { errCh <- decoder.Decode(&ready) }()
	select {
	case err = <-errCh:
	case <-time.After(30 * time.Second):
		err = fmt.Errorf("OPC-UA peer startup timed out")
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		cleanup()
		return 0, nil, nil, fmt.Errorf("independent OPC-UA peer: %w", err)
	}
	publish := func(sequence uint32) (int64, error) {
		if err := encoder.Encode(map[string]uint32{"sequence": sequence}); err != nil {
			return 0, err
		}
		var response struct {
			Sequence uint32 `json:"sequence"`
			Count    int64  `json:"count"`
		}
		if err := decoder.Decode(&response); err != nil {
			return 0, err
		}
		if response.Sequence != sequence || response.Count != 100 {
			return 0, io.ErrUnexpectedEOF
		}
		return response.Count, nil
	}
	return ready.Namespace, publish, cleanup, nil
}
