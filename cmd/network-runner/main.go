package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/containers/gvisor-tap-vsock/pkg/notification"
	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

// runnerConfig mirrors the networkRunnerConfig in internal/provider/resource_network.go.
// Keep these two definitions in sync.
type runnerConfig struct {
	types.Configuration
	Socket             string `json:"socket"`
	NotificationSocket string `json:"notification_socket,omitempty"`
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	var notifySocket string

	cmd := &cobra.Command{
		Use:     "network-runner [flags] <path-to-config.json|->",
		Short:   "A userspace NAT network daemon for MacVF instances",
		Long:    "A userspace NAT network daemon for MacVF instances, backed by gvisor-tap-vsock.",
		Version: version,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(args[0], notifySocket)
		},
	}
	cmd.Flags().StringVarP(&notifySocket, "notification", "n", "", "Unix socket path for ready/error notifications (overrides config field).")

	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func run(configFile, notifySocket string) error {
	var configData []byte
	var err error
	if configFile == "-" {
		configData, err = io.ReadAll(os.Stdin)
	} else {
		configData, err = os.ReadFile(configFile)
	}
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	var cfg runnerConfig
	if err := json.Unmarshal(configData, &cfg); err != nil {
		return fmt.Errorf("failed to unmarshal config %s: %w", configFile, err)
	}

	if notifySocket != "" {
		cfg.NotificationSocket = notifySocket
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Build a notification sender. NewNotificationSender returns a no-op
	// sender when the socket path is empty, so downstream Send() calls are
	// always safe.
	notifier := notification.NewNotificationSender(cfg.NotificationSocket)
	if cfg.NotificationSocket != "" {
		go notifier.Start(ctx)
	}

	vn, err := virtualnetwork.New(&cfg.Configuration)
	if err != nil {
		notifier.Send(types.NotificationMessage{NotificationType: types.HypervisorError})
		return fmt.Errorf("failed to create virtual network: %w", err)
	}
	vn.SetNotificationSender(notifier)

	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: cfg.Socket, Net: "unixgram"})
	if err != nil {
		notifier.Send(types.NotificationMessage{NotificationType: types.HypervisorError})
		return fmt.Errorf("failed to listen on unixgram socket %s: %w", cfg.Socket, err)
	}
	defer os.Remove(cfg.Socket)

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	log.Printf("Network ready on unixgram socket %s", cfg.Socket)
	notifier.Send(types.NotificationMessage{NotificationType: types.Ready})

	if err := acceptVfkit(ctx, vn, listener); err != nil && !errors.Is(err, net.ErrClosed) {
		notifier.Send(types.NotificationMessage{NotificationType: types.HypervisorError})
		log.Printf("vfkit accept error: %v", err)
	}

	log.Println("Virtual network shut down.")
	return nil
}

// acceptVfkit replicates upstream gvproxy's vfkit accept path: it peeks the
// first datagram to learn vfkit's autobind address, then hands the listener
// to AcceptVfkit wrapped so writes go back to that peer. Upstream lives in
// gvisor-tap-vsock cmd/gvproxy and pkg/transport/unixgram_unix.go; this
// project's vendored gvisor-tap-vsock predates the helper, so the code is
// reproduced here.
func acceptVfkit(ctx context.Context, vn *virtualnetwork.VirtualNetwork, listener *net.UnixConn) error {
	peer, err := peekVfkitAddress(listener)
	if err != nil {
		return fmt.Errorf("peek vfkit peer: %w", err)
	}

	conn, err := newConnectedUnixgramConn(listener, peer)
	if err != nil {
		return fmt.Errorf("wrap unixgram conn: %w", err)
	}
	return vn.AcceptVfkit(ctx, conn)
}

// connectedUnixgramConn wraps a listening unixgram socket so Write goes to a
// fixed peer address (the vfkit virtio-net side). This is needed because
// net.ListenUnixgram returns an unconnected socket whose Write would fail.
type connectedUnixgramConn struct {
	*net.UnixConn
	peer *net.UnixAddr
}

func newConnectedUnixgramConn(conn *net.UnixConn, peer *net.UnixAddr) (*connectedUnixgramConn, error) {
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	var sockErr error
	err = rawConn.Control(func(fd uintptr) {
		if sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, 1*1024*1024); sockErr != nil {
			return
		}
		sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4*1024*1024)
	})
	if err != nil {
		return nil, err
	}
	if sockErr != nil {
		return nil, sockErr
	}
	return &connectedUnixgramConn{UnixConn: conn, peer: peer}, nil
}

func (c *connectedUnixgramConn) RemoteAddr() net.Addr        { return c.peer }
func (c *connectedUnixgramConn) Write(b []byte) (int, error) { return c.WriteTo(b, c.peer) }

// peekVfkitAddress blocks until vfkit sends its first datagram, then returns
// its bound address without consuming the packet. If the packet is the legacy
// "VFKT" handshake it is consumed.
func peekVfkitAddress(conn *net.UnixConn) (*net.UnixAddr, error) {
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	magic := make([]byte, 4)
	var sockaddr syscall.Sockaddr
	var recvErr error
	if err := rawConn.Read(func(fd uintptr) bool {
		_, sockaddr, recvErr = syscall.Recvfrom(int(fd), magic, syscall.MSG_PEEK|syscall.MSG_TRUNC)
		return !errors.Is(recvErr, syscall.EAGAIN)
	}); err != nil {
		return nil, err
	}
	if recvErr != nil {
		return nil, recvErr
	}
	if bytes.Equal(magic, []byte("VFKT")) {
		if _, _, err := conn.ReadFrom(magic); err != nil {
			return nil, err
		}
	}
	unixSockaddr, ok := sockaddr.(*syscall.SockaddrUnix)
	if !ok {
		return nil, fmt.Errorf("unexpected remote address type %T", sockaddr)
	}
	if unixSockaddr.Name == "" {
		return nil, fmt.Errorf("vfkit peer address is empty")
	}
	return &net.UnixAddr{Name: unixSockaddr.Name, Net: "unixgram"}, nil
}
