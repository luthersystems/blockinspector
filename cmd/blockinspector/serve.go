package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/luthersystems/blockinspector/internal/explorer"
	"github.com/luthersystems/blockinspector/internal/peerclient"
)

// peerConfig builds the peer client's config from --network-dir and the
// explicit flags, which override it.
func peerConfig(cmd Serve) (peerclient.Config, error) {
	var cfg peerclient.Config
	if cmd.NetworkDir != "" {
		var err error
		cfg, err = peerclient.NetworkConfig(peerclient.NetworkOptions{
			Dir:     cmd.NetworkDir,
			Org:     cmd.Org,
			Peer:    cmd.Peer,
			User:    cmd.User,
			Address: cmd.PeerAddress,
			Channel: cmd.Channel,
		})
		if err != nil {
			return cfg, err
		}
	}
	cfg.Address = cmd.PeerAddress
	cfg.Channel = cmd.Channel
	for _, o := range []struct {
		dst *string
		v   string
	}{
		{&cfg.ServerName, cmd.ServerName},
		{&cfg.TLSRootCAFile, cmd.TLSRootCA},
		{&cfg.TLSCertFile, cmd.TLSCert},
		{&cfg.TLSKeyFile, cmd.TLSKey},
		{&cfg.MSPID, cmd.MSPID},
		{&cfg.SignCertFile, cmd.SignCert},
		{&cfg.SignKeyFile, cmd.SignKey},
	} {
		if o.v != "" {
			*o.dst = o.v
		}
	}
	if cfg.TLSRootCAFile == "" || cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" || cfg.MSPID == "" || cfg.SignCertFile == "" || cfg.SignKeyFile == "" {
		return cfg, errors.New("give --network-dir, or --tls-root-ca, --tls-cert, --tls-key, --msp-id, --sign-cert and --sign-key, or --ledger-dir")
	}
	return cfg, nil
}

func serve(cmd Serve) error {
	var src explorer.Source
	if cmd.LedgerDir != "" {
		if cmd.NetworkDir != "" {
			return errors.New("give --ledger-dir or --network-dir, not both")
		}
		src = &explorer.LedgerSource{Dir: cmd.LedgerDir, Channel: cmd.Channel, Orderer: cmd.Orderer}
	} else {
		cfg, err := peerConfig(cmd)
		if err != nil {
			return err
		}
		client, err := peerclient.New(cfg)
		if err != nil {
			return err
		}
		src = peerclient.Source{Client: client}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ix := explorer.NewIndex(cmd.Channel)
	follower := explorer.NewFollower(src, ix)
	api := explorer.NewServer(ix, follower.Status, explorer.ServerOptions{AllowOrigin: cmd.AllowOrigin})

	ln, err := net.Listen("tcp", cmd.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           api,
		ReadHeaderTimeout: 10 * time.Second,
		// The event streams end when the process is told to stop.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() {
		if err := follower.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logrus.WithError(err).Error("block source stopped")
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx) // the open event streams end with the process
	}()
	fmt.Fprintf(os.Stderr, "block explorer API on http://%s/v1/status (source %s, channel %s)\n", ln.Addr(), src.Name(), cmd.Channel)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
