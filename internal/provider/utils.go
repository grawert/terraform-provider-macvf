package provider

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	bytesize "github.com/zpatrick/go-bytesize"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

const (
	macMulticastBit        = byte(0x01)
	macLocallyAdministered = byte(0x02)
)

var bytesizeUnits = map[string]bytesize.Bytesize{
	"B":   bytesize.B,
	"KB":  bytesize.KB,
	"MB":  bytesize.MB,
	"GB":  bytesize.GB,
	"TB":  bytesize.TB,
	"PB":  bytesize.PB,
	"EB":  bytesize.EB,
	"KIB": bytesize.KiB,
	"MIB": bytesize.MiB,
	"GIB": bytesize.GiB,
	"TIB": bytesize.TiB,
	"PIB": bytesize.PiB,
	"EIB": bytesize.EiB,
}

// parseBytesize parses a human-readable size string into a bytesize.Bytesize.
// Accepted suffixes: B, KB, MB, GB, TB, PB, EB (decimal, 1000-based) and
// KiB, MiB, GiB, TiB, PiB, EiB (binary, 1024-based). Case-insensitive.
func parseBytesize(s string) (bytesize.Bytesize, error) {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, func(r rune) bool {
		return r < '0' || r > '9'
	})
	if i <= 0 {
		return 0, fmt.Errorf("invalid size %q: must be a number followed by a unit suffix (e.g. \"20GB\", \"2GiB\")", s)
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	unit, ok := bytesizeUnits[strings.ToUpper(strings.TrimSpace(s[i:]))]
	if !ok {
		return 0, fmt.Errorf("invalid size %q: unrecognized unit %q", s, s[i:])
	}
	return bytesize.Bytesize(n) * unit, nil
}

type positiveInt64Validator struct{}

func (v positiveInt64Validator) Description(_ context.Context) string {
	return "Value must be at least 1."
}

func (v positiveInt64Validator) MarkdownDescription(_ context.Context) string {
	return "Value must be at least 1."
}

func (v positiveInt64Validator) ValidateInt64(_ context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if req.ConfigValue.ValueInt64() < 1 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", "Must be at least 1.")
	}
}

type cidrValidator struct{}

func (v cidrValidator) Description(_ context.Context) string {
	return "Value must be valid CIDR notation (e.g. 192.168.100.0/24)."
}

func (v cidrValidator) MarkdownDescription(_ context.Context) string {
	return "Value must be valid CIDR notation (e.g. `192.168.100.0/24`)."
}

func (v cidrValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, _, err := net.ParseCIDR(req.ConfigValue.ValueString()); err != nil {
		message := fmt.Sprintf("%q is not valid CIDR notation: %s", req.ConfigValue.ValueString(), err)
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid CIDR notation", message)
	}
}

// isProcessAlive reports whether a process with the given PID is currently
// running. On Unix, signal 0 probes the process without actually delivering a
// signal — it returns nil if the process exists and we have permission to
// signal it.
func isProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// terminateProcess sends SIGTERM and waits up to gracePeriod for the process
// to exit. If the process is still alive after gracePeriod, it sends SIGKILL.
// Returns an error only if SIGTERM itself could not be delivered.
func terminateProcess(pid int, gracePeriod time.Duration) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		if err == syscall.ESRCH {
			return nil // already gone
		}
		return err
	}
	deadline := time.Now().Add(gracePeriod)
	for time.Now().Before(deadline) {
		if process.Signal(syscall.Signal(0)) != nil {
			return nil // exited cleanly
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = process.Signal(syscall.SIGKILL)
	return nil
}

// generateLocalMAC creates a locally administered unicast MAC address.
// If prefix has at least 3 bytes, those bytes seed the OUI (the first byte's
// bits are adjusted to enforce locally-administered unicast); the last 3 bytes
// are always cryptographically random. Pass nil for a fully random MAC.
func generateLocalMAC(prefix []byte) (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[3:]); err != nil {
		return "", fmt.Errorf("failed to generate random MAC bytes: %w", err)
	}
	if len(prefix) >= 3 {
		b[0] = (prefix[0] &^ macMulticastBit) | macLocallyAdministered
		b[1] = prefix[1]
		b[2] = prefix[2]
	} else {
		if _, err := rand.Read(b[:3]); err != nil {
			return "", fmt.Errorf("failed to generate random MAC bytes: %w", err)
		}
		b[0] = (b[0] &^ macMulticastBit) | macLocallyAdministered
	}
	return net.HardwareAddr(b[:]).String(), nil
}
