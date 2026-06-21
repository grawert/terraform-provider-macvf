# Architecture: Terraform Provider for macOS Native Virtualization (terraform-provider-macvf)

## Project Overview & Goal
A custom Terraform provider (`terraform-provider-macvf`) for macOS that utilises Apple's native `Virtualization.framework` via the `vfkit` CLI utility.

To ensure robust state management and process lifecycle, the provider:

1. Orchestrates the **`vfkit`** CLI utility to manage the lifecycle of virtual machines as detached processes.
2. Embeds a pre-built **`network-runner`** binary inside the provider binary. At runtime the provider extracts it to a cache directory and launches it as a detached process to handle virtual networking (DHCP, DNS) via `gvisor-tap-vsock` internally.

---

## Architectural Blueprint

### VM Hypervisor (`vfkit`)
* Interaction via a CLI wrapper inside the provider ([resource_instance.go](../internal/provider/resource_instance.go)).
* Each VM is launched as a separate, detached `vfkit` process.
* The provider stores the process PID in Terraform state for lifecycle management.
* On Read, if the stored PID is no longer alive, the resource is removed from state (triggering a recreate on next apply). No process-title scanning is performed.

### Advanced Networking (`network-runner` / `gvisor-tap-vsock`)
* A `network-runner` binary is embedded inside the provider binary (at `internal/provider/embedded/network-runner`) and extracted to `<cache_dir>/bin/network-runner` on first use ([networkrunner.go](../internal/provider/networkrunner.go)).
* On resource creation, the provider serialises a JSON config and pipes it to `network-runner -` via stdin. No config file is written to disk.
* `network-runner` exposes a `SOCK_DGRAM` Unix domain socket at `socket_path` that `vfkit` connects to via `--device virtio-net,unixSocketPath=<path>`.
* Handles static DHCP leases (IP → MAC) and acts as the local DNS server for A-records and search domains defined in the HCL configuration.
* On Read, if the stored PID is no longer alive, the resource is removed from state. Since the network config is not persisted to disk, a dead network-runner process cannot be recovered and must be recreated.
* An ephemeral Unix notification socket (`/tmp/macvf-notify-<rand>.sock`) is created for the duration of Create only. `network-runner` dials it and sends `{"notification_type":"ready"}` once the network stack is fully initialised. The socket is removed as soon as the notification arrives (or on timeout/error).

### Provider Configuration
* `cache_dir` — optional attribute controlling where the extracted provider binaries (`vfkit`, `network-runner`) are cached. Defaults to `~/Library/Caches/<provider-name>`.
* `vfkit_path` — optional override pointing to a specific `vfkit` installation; when unset the embedded binary is used.
* Storage pools (`macvf_pool`) and disk images (`macvf_disk`) are placed wherever the user specifies via `path`/`pool_id`. No provider-wide data directory is required.

### State Storage
All resource state is stored directly in Terraform's own state file. No separate metadata files, config files, or JSON sidecars are written. The only file artefacts created on disk by the provider are:

| Artefact | Location | Lifetime |
|----------|----------|---------|
| Extracted `vfkit` binary | `<cache_dir>/bin/vfkit` | Persistent across runs |
| Extracted `network-runner` binary | `<cache_dir>/bin/network-runner` | Persistent across runs |
| Network-runner Unix socket | `socket_path` (default `/tmp/<provider>-<name>-<rand8>.sock`) | Exists while network-runner is running; removed on Delete |
| Network-runner log | `<socket_path>.log` | Created on network Create; removed on Delete |
| Notification socket | `/tmp/macvf-notify-<rand>.sock` | Ephemeral — deleted at end of Create |
| Disk image | `<pool_path>/<name>.raw` | Exists until disk Delete |
| EFI variable store | `<boot_disk_dir>/<instance_name>.efivars.fd` | Created on EFI-boot instance Create; removed on Delete |
| Cloud-init files | `<pool_path>/<name>.cloud-init/{user-data,meta-data,[network-config]}` | Exists until cloudinit_disk Delete |

---

## Resources

### `macvf_pool` — Storage Pool
* **Create:** Creates the specified directory path on the macOS host.
* **Read:** Checks the directory exists; removes from state if not.
* **Delete:** Removes the directory and its contents (`os.RemoveAll`).
* Schema: `name` (required, replace), `path` (required, replace), `id` (computed — equals `path`).

### `macvf_disk` — Virtual Disk Image
* **Create:** Generates a `.raw` sparse disk image inside the pool path. Three modes:
  * Blank disk — `size_gb` only: creates a sparse file via `truncate(2)`.
  * Copy from source — `source`: clones via `clonefileat(2)` (APFS CoW), falls back to byte copy.
  * Clone from existing disk — `base_disk_id`: same clone logic as `source`.
  * Optional `size_gb` with `source`/`base_disk_id` grows the disk after copy; shrinking is rejected.
* **Read:** Checks the file exists; removes from state if not.
* **Delete:** Removes the `.raw` file.
* Schema: `name`, `pool_id`, `size_gb` (optional), `source` (optional), `base_disk_id` (optional), `id` (computed — full path to the `.raw` file).

### `macvf_network` — Virtual Network
* **Create:** Builds a `gvisor-tap-vsock` JSON config and pipes it to `network-runner -` via stdin. Waits synchronously (up to `startup_timeout_seconds`) for the `ready` notification over an ephemeral Unix socket before committing state. Treats `hypervisor_error` or timeout as failure and kills the child.
* **Read:** Verifies the `network-runner` process is alive via `kill -0`; removes from state if the PID is gone.
* **Delete:** Sends SIGTERM (5 s grace period, then SIGKILL) to the process; removes the Unix socket file and its sidecar log.
* Schema:

| Attribute | Type | Notes |
|-----------|------|-------|
| `name` | string | Required. Triggers replace. |
| `subnet` | string | Required, CIDR-validated. Triggers replace. |
| `gateway_ip` | string | Optional/Computed. Defaults to first host address in the subnet (e.g. `192.168.100.1`). Triggers replace. |
| `gateway_mac` | string | Optional/Computed. Auto-generated as a stable locally-administered MAC derived from a SHA-256 of the network name plus a random suffix. Triggers replace. |
| `socket_path` | string | Optional/Computed. Default `/tmp/<provider>-<name>-<rand8>.sock` (random suffix generated at Create time, stored in state). The `SOCK_DGRAM` Unix socket vfkit connects to. Triggers replace. |
| `startup_timeout_seconds` | int | Optional/Computed. Default `10`, range 1–600. |
| `pid` | int | Computed. PID of the running network-runner process. |
| `dns_search_domains` | list(string) | Optional. Search domains pushed to VMs via DHCP. |
| `lease` (block) | list | Optional. Reserved network identity per VM — co-locates `hostname`, `ip_address`, `mac_address`. Expands into both a DHCP static lease (IP→MAC) and a DNS A-record on the embedded gateway. `mac_address` is auto-generated if omitted. All fields trigger replace. |
| `port_forward` (block) | list | Optional. TCP port forwarding from host into the virtual network. `host` and `vm` are `host:port` strings. Triggers replace. |

* The network resource `id` is the socket path.

### `macvf_instance` — Virtual Machine
* **Create:** Synthesises `vfkit` CLI arguments and launches it as a detached process. Waits up to 3 seconds: if vfkit exits within that window it is treated as a startup failure (bad args, missing firmware, etc.) and the error is surfaced with the stderr content. After 3 seconds without exit the process is detached.
* **Read:** Checks the stored PID with `kill -0`; removes from state if the process is gone.
* **Delete:** Sends SIGTERM (10 s grace period, then SIGKILL) to the process; removes the EFI variable store file for EFI-boot instances.
* Boot options (mutually exclusive):
  * **EFI boot** (default when `linux_boot` block is absent) — NVRAM stored at `<boot_disk_dir>/<name>.efivars.fd` (alongside the boot disk in its pool).
  * **`linux_boot { kernel_path, initrd_path, cmdline }`** — direct kernel boot via `--bootloader linux,...`. The kernel must be uncompressed on Apple Silicon (arm64 requirement from `Virtualization.framework`).
* vfkit stderr is always captured to a sidecar log. Default path: `<console_log_dir>/<name>-vfkit.log` when `console_log_path` is set, otherwise `$TMPDIR/<name>-vfkit.log`. Can be overridden with `vfkit_log_path`.
* Schema (top-level attributes): `name` (replace), `vcpus` (replace), `memory_mb` (replace), `pid` (computed), `console_log_path` (optional, replace), `vfkit_log_path` (optional, replace), `cloud_init_disk_id` (optional, replace).
* Schema (blocks): `linux_boot` (optional, single), `network_interface { type, network_id, mac_address }`, `disk_attachment { disk_id, is_boot }`.
* Network interfaces: `type = "gvisor"` (default) connects via `--device virtio-net,unixSocketPath=<socket_path>,mac=<mac>`; `type = "nat"` uses `--device virtio-net,nat,mac=<mac>`. MAC addresses are auto-generated and stored in state for stability.
* Cloud-init: when `cloud_init_disk_id` is set to the `id` of a `macvf_cloudinit_disk` resource, the provider passes `--cloud-init <user-data>,<meta-data>[,<network-config>]` to vfkit, which builds the ISO internally.

### `macvf_cloudinit_disk` — Cloud-Init Configuration Directory
* Writes plain-text cloud-init configuration files to a directory inside a storage pool. vfkit's `--cloud-init` flag consumes these files and builds the ISO image internally.
* **Create:** Creates `<pool_id>/<name>.cloud-init/` and writes `user-data`, `meta-data` (always, even if empty — required by the NoCloud datasource), and optionally `network-config`.
* **Read:** Checks that `user-data` exists; removes from state if not.
* **Delete:** Removes the entire `.cloud-init` directory.
* Schema: `name` (required, replace), `pool_id` (required, replace), `user_data` (required, replace), `meta_data` (optional, replace), `network_config` (optional, replace), `id` (computed — path to the `.cloud-init` directory), `user_data_path` (computed), `meta_data_path` (computed), `network_config_path` (computed — empty string when `network_config` is not set).
* Use `cloud_init_disk_id = macvf_cloudinit_disk.<name>.id` on `macvf_instance` to wire it up.

---

## Target Terraform Schema (HCL Example)

```hcl
terraform {
  required_providers {
    macvf = {
      source = "buschtaxi/macvf"
    }
  }
}

provider "macvf" {
  # cache_dir = "~/Library/Caches/macvf"  # optional; controls where vfkit/network-runner binaries are extracted
  # vfkit_path = "/usr/local/bin/vfkit"   # optional; use a specific vfkit installation
}

resource "macvf_pool" "data" {
  name = "vm-data"
  path = pathexpand("~/vms/data")
}

resource "macvf_disk" "ubuntu_root" {
  name    = "ubuntu-root"
  pool_id = macvf_pool.data.id
  size_gb = 20
}

resource "macvf_network" "vm_net" {
  name   = "my-vm-network"
  subnet = "192.168.100.0/24"

  # gateway_ip = "192.168.100.1"                    # optional; defaults to first host address
  # gateway_mac = "5a:xx:xx:xx:xx:xx"               # optional; auto-generated from network name
  # socket_path = "/tmp/macvf-my-vm-network.sock"   # optional; defaults to /tmp/<provider>-<name>.sock
  # startup_timeout_seconds = 30                     # optional; defaults to 10

  lease {
    hostname    = "myhost.local"
    ip_address  = "192.168.100.10"
    mac_address = "52:54:00:aa:bb:cc"  # optional; auto-generated if omitted
  }

  port_forward {
    host = "127.0.0.1:2222"
    vm   = "192.168.100.10:22"
  }

  dns_search_domains = ["example.com", "internal.local"]
}

resource "macvf_cloudinit_disk" "vm_init" {
  name    = "initial-config"
  pool_id = macvf_pool.data.id

  user_data = <<-EOT
    #cloud-config
    users:
      - name: macvfuser
        sudo: ALL=(ALL) NOPASSWD:ALL
        ssh_authorized_keys:
          - ssh-rsa AAAA...
  EOT

  meta_data = <<-EOT
    instance-id: my-ubuntu-vm
    local-hostname: ubuntu-vm
  EOT

  # network_config = "..."  # optional
}

resource "macvf_instance" "ubuntu_vm" {
  name      = "ubuntu-server"
  vcpus     = 2
  memory_mb = 2048

  cloud_init_disk_id = macvf_cloudinit_disk.vm_init.id

  # linux_boot {
  #   kernel_path = "/path/to/vmlinuz"   # must be uncompressed on Apple Silicon
  #   initrd_path = "/path/to/initrd.img"
  #   cmdline     = "console=hvc0 root=/dev/vda rw"
  # }

  network_interface {
    network_id  = macvf_network.vm_net.id
    mac_address = macvf_network.vm_net.lease[0].mac_address
  }

  disk_attachment {
    disk_id = macvf_disk.ubuntu_root.id
    is_boot = true
  }
}
```

---

## Technical References

* Terraform Plugin Framework: https://github.com/hashicorp/terraform-plugin-framework
* vfkit: https://github.com/crc-org/vfkit
* gvisor-tap-vsock: https://github.com/containers/gvisor-tap-vsock
