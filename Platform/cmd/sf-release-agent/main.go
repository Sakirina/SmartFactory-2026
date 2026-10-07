package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"competition2026/product/platform/internal/releaseagent"
)

func main() {
	var o releaseagent.Options
	flag.StringVar(&o.CloudURL, "cloud", "", "cloud release authority URL")
	flag.StringVar(&o.ConfigURL, "config", "", "independent configuration center URL")
	flag.StringVar(&o.Directory, "dir", "", "private persistent node directory")
	flag.StringVar(&o.NodeID, "node-id", "", "registered node identity")
	flag.StringVar(&o.Listen, "listen", "127.0.0.1:8091", "application listen address")
	flag.StringVar(&o.TokenFile, "token-file", "", "private workload credential file")
	flag.StringVar(&o.BootstrapPasswordFile, "bootstrap-password-file", "", "private initial local administrator password file")
	flag.StringVar(&o.TLSCA, "ca", "", "service CA")
	flag.StringVar(&o.TLSCertificate, "cert", "", "workload client certificate")
	flag.StringVar(&o.TLSKey, "key", "", "workload client key")
	flag.StringVar(&o.DataTransferAddress, "datatransfer", "", "local DataTransfer consumer")
	flag.StringVar(&o.RuntimeConfigFile, "runtime-config", "", "private runtime settings retaining database, master key and service integration")
	flag.BoolVar(&o.Seed, "seed", false, "load editable local factory data")
	prepareOnly := flag.Bool("prepare-only", false, "authenticate and cache the first release while the original Edge remains online")
	flag.DurationVar(&o.PollInterval, "poll", time.Second, "desired-state polling interval")
	flag.Parse()
	a, e := releaseagent.New(o)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *prepareOnly {
		e = a.PrepareCurrent(ctx)
	} else {
		e = a.Run(ctx)
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
