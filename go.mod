module terraform-provider-macvf

go 1.25.5

require (
	github.com/containers/gvisor-tap-vsock v0.8.9
	github.com/hashicorp/terraform-plugin-framework v1.4.2
	github.com/hashicorp/terraform-plugin-log v0.9.0
	github.com/spf13/cobra v1.10.2
	github.com/zpatrick/go-bytesize v0.0.0-20170214182126-40b68ac70b6a
)

require (
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/apparentlymart/go-cidr v1.1.1 // indirect
	github.com/fatih/color v1.13.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/google/gopacket v1.1.19 // indirect
	github.com/hashicorp/go-hclog v1.5.0 // indirect
	github.com/hashicorp/go-plugin v1.5.1 // indirect
	github.com/hashicorp/go-uuid v1.0.3 // indirect
	github.com/hashicorp/terraform-plugin-go v0.19.0 // indirect
	github.com/hashicorp/terraform-registry-address v0.2.2 // indirect
	github.com/hashicorp/terraform-svchost v0.1.1 // indirect
	github.com/hashicorp/yamux v0.0.0-20180604194846-3520598351bb // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/inetaf/tcpproxy v0.0.0-20250222171855-c4b9df066048 // indirect
	github.com/insomniacslk/dhcp v0.0.0-20240710054256-ddd8a41251c9 // indirect
	github.com/mattn/go-colorable v0.1.12 // indirect
	github.com/mattn/go-isatty v0.0.14 // indirect
	github.com/miekg/dns v1.1.72 // indirect
	github.com/mitchellh/go-testing-interface v1.14.1 // indirect
	github.com/oklog/run v1.0.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.14 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/u-root/uio v0.0.0-20240224005618-d2acac8f3701 // indirect
	github.com/vmihailenco/msgpack/v5 v5.3.5 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/exp v0.0.0-20231110203233-9a3e6036ecaa // indirect
	golang.org/x/mod v0.36.0 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	golang.org/x/time v0.12.0 // indirect
	golang.org/x/tools v0.44.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250818200422-3122310a409c // indirect
	google.golang.org/grpc v1.75.1 // indirect
	google.golang.org/protobuf v1.36.8 // indirect
	gvisor.dev/gvisor v0.0.0-20260413194555-9680d69bf798 // indirect
)

// gvisor-tap-vsock v0.8.9 was built against an older gvisor.dev/gvisor that
// used a void UDP ForwarderHandler. The rest of the dep tree pulls in a
// newer gvisor whose ForwarderHandler returns bool, which makes v0.8.9 fail
// to compile. Upstream main already adopted the new signature and added the
// notification feature this provider depends on; track main until the next
// tagged release (>= v0.8.10) ships and we can drop the replace.
replace github.com/containers/gvisor-tap-vsock => github.com/containers/gvisor-tap-vsock v0.0.0-20260519151820-e0561ea366a0
