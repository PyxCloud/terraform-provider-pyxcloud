//go:build !linux

package runtime

// nitro_nsm_other.go is the non-linux fallback for the NSM generation path. The
// Nitro Security Module is linux-only, so on any other OS (e.g. a developer's
// macOS workstation or a non-linux CI runner) the real document generation simply
// degrades cleanly with a documented error — never a silent fake. The verification
// path (nitro.go) stays portable so the known-answer test runs everywhere.

import "errors"

// errNoNSM is the documented degradation error on platforms without NSM support.
var errNoNSM = errors.New("nitro NSM device unavailable (NSM is linux-only; not running inside a Nitro enclave)")

// nitroNSMAvailable is always false off linux.
func nitroNSMAvailable() bool { return false }

// generateNitroAttestation always degrades cleanly off linux.
func generateNitroAttestation(_, _ []byte) ([]byte, error) { return nil, errNoNSM }
