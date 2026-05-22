# Terraform provider for macOS Virtualization Framework

This provider allows managing macOS Virtualization Framework (MacVF) resources
(virtual machines, storage pools, networks, disks, cloud-init) using Terraform.
It leverages `vfkit` and `network-runner` to define, configure, and manage
virtualization resources on macOS hosts. Since macOS Virtualization Framework
is used it supports arm64 (aarch64) images only.
The provider seamlessly integrates with `vfkit` for VM lifecycle management and
`gvisor-tap-vsock` (via `network-runner`) for virtual networking.

## Resource types

| Resource               | Description                                                 |
| :--------------------- | :---------------------------------------------------------- |
| `macvf_instance`       | Manages a virtual machine instance.                         |
| `macvf_network`        | Configures a virtual network for instances.                 |
| `macvf_pool`           | Manages a directory-based storage pool.                     |
| `macvf_disk`           | Manages a raw virtual disk image within a storage pool.     |
| `macvf_cloudinit_disk` | Writes cloud-init files (user-data, meta-data, network-config) to a directory. |

## Using the provider

Both `vfkit` and `network-runner` are bundled inside the provider binary and
extracted automatically at runtime — no manual installation required. Note that
this provider only supports Apple Silicon (darwin/arm64).

```hcl
terraform {
  required_providers {
    macvf = {
      source = "bushtaxi/macvf"
    }
  }
}

provider "macvf" {
  # Optional: override the binary cache directory (default: ~/Library/Caches/macvf)
  # cache_dir = pathexpand("~/Library/Caches/macvf")

  # Optional: use your own vfkit instead of the embedded one
  # vfkit_path = "/opt/homebrew/bin/vfkit"
}

resource "macvf_pool" "data" {
  name = "vm-data"
  path = pathexpand("~/Library/Application Support/macvf/data")
}

resource "macvf_disk" "base_image" {
  name    = "fedora-41-base"
  pool_id = macvf_pool.data.id
  source  = pathexpand("~/Downloads/fedora-41.raw")
}

resource "macvf_disk" "root" {
  name         = "vm-root"
  pool_id      = macvf_pool.data.id
  base_disk_id = macvf_disk.base_image.id
  size         = "20GB"
}

resource "macvf_cloudinit_disk" "cloudinit" {
  name    = "vm-cloudinit"
  pool_id = macvf_pool.data.id
  user_data = <<-EOT
    #cloud-config
    users:
      - name: admin
        sudo: ALL=(ALL) NOPASSWD:ALL
        ssh_authorized_keys:
          - ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC...
    EOT
  meta_data = <<-EOT
    instance-id: my-vm
    local-hostname: my-vm
    EOT
  network_config = <<-EOT
    network:
      version: 2
      ethernets:
        eth0:
          dhcp4: true
    EOT
}

resource "macvf_network" "vm_net" {
  name   = "vm-network"
  subnet = "192.168.100.0/24"

  # Generated randomly by default.
  # gateway_mac = "5a:xx:xx:xx:xx:xx"

  # Override if /tmp is unsuitable.
  # socket_path = "/var/run/macvf/vm-network.sock"

  # Capture network-runner stdout/stderr for debugging. Not created by default.
  # log_path = "${path.module}/vm-network.log"

  # Increase if your host is slow to launch network-runner.
  # startup_timeout_seconds = 30

  # The MAC must be set on the VM's network_interface so DHCP and DNS
  # hand it the IP and hostname declared here. Omit `mac_address` for
  # auto-generated mac address.
  lease {
    hostname    = "my-vm.local"
    ip_address  = "192.168.100.10"
    mac_address = "52:54:00:aa:bb:cc"
  }

  # Search domains pushed to VMs via DHCP.
  dns_search_domains = ["example.com", "internal.local"]

  # The VM subnet is userspace NAT; port_forward is the only way to reach a VM from the host.
  port_forward {
    host = "127.0.0.1:2222"
    vm   = "192.168.100.10:22"
  }
}

resource "macvf_instance" "vm" {
  name      = "my-vm"
  vcpus     = 2
  memory    = "2GiB"

  # Serial console output written to a file in the Terraform project directory.
  # Tail it while the VM boots: tail -f ./my-vm-console.log
  console_log_path = "${path.module}/my-vm-console.log"

  # vfkit's own log (startup errors, device warnings).
  vfkit_log_path = "${path.module}/my-vm-vfkit.log"

  # cloud-init
  cloud_init_disk_id = macvf_cloudinit_disk.cloudinit.id

  # If you define a `linux_boot` block, it will be used instead of EFI boot.
  # linux_boot {
  #   kernel_path = "/path/to/your/vmlinuz"
  #   initrd_path = "/path/to/your/initrd.img"
  #   cmdline     = "console=ttyS0 root=/dev/vda rw"
  # }

  # gvisor (default): full userspace NAT via macvf_network — supports port
  # forwarding, static DHCP, and custom DNS; VM is reachable from the host.
  network_interface {
    network_id  = macvf_network.vm_net.id
    mac_address = macvf_network.vm_net.lease[0].mac_address
  }

  # nat: Apple's built-in NAT — zero-config outbound internet, no macvf_network
  # required, but the VM is not reachable from the host.
  # network_interface {
  #   type = "nat"
  # }

  disk_attachment {
    disk_id = macvf_disk.root.id
    is_boot = true
  }
}
```

## Development Information

### Design Principles

- **MacOS Virtualization Framework Focus**: This provider is built specifically
  for managing virtual machines on macOS using Apple's Virtualization Framework,
  often interacting with the `vfkit` utility.
- **Terraform Plugin Framework**: Developed using the latest Terraform Plugin
  Framework for modern features and stability.
- **Minimal Abstraction**: Resources aim to expose underlying `vfkit` and
  `gvisor-tap-vsock` concepts directly, providing granular control to users.

### Building and running the provider from source

**Prerequisites**:
-   Go (version 1.25 or later)

```bash
make build
```

Run `make` without arguments to see all available targets.

To install the provider locally for development, override it in `$HOME/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "registry.terraform.io/bushtaxi/macvf" = "/path/to/your/cloned/repo"
  }

  # For all other providers, install them directly from their origin provider
  # registries as normal.
  direct {}
}
```

### Releasing

**Via CI (recommended):**

```bash
make release VERSION=1.2.3
```

Creates and pushes the annotated git tag `v1.2.3`. The GitHub Actions release
workflow triggers automatically and runs `make goreleaser`.

**From a local machine:**

```bash
make tag VERSION=1.2.3
make goreleaser
```

### Running tests

```bash
make lint      # Run linter
make test      # Run unit tests
make testacc   # Run acceptance tests (requires vfkit, network-runner, and macOS host)
```

## Contributing

Feel free to open pull requests if you`ve found a bug or want to add a feature.

## License

*   [MIT](LICENSE)
