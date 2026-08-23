package agentcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// elfHeader builds a minimal ELF header for a given machine type. Only the
// magic and e_machine matter to the check.
func elfHeader(machine byte) []byte {
	h := make([]byte, 64)
	copy(h, elfMagic)
	h[elfMachineOffset] = machine
	return h
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestValidateRuntimeBinary_AcceptsARM64(t *testing.T) {
	path := writeTemp(t, "runtime", elfHeader(elfMachineARM64))

	if errs := validateRuntimeBinary(path); len(errs) != 0 {
		t.Errorf("validateRuntimeBinary = %v, want no errors for an arm64 ELF", errs)
	}
}

// The failure this exists for: an x86-64 binary uploads, deploys and reports
// success, then cannot exec inside the runtime. The runtime never reaches
// READY and nothing in the output points at the architecture.
func TestValidateRuntimeBinary_RejectsTheWrongArchitecture(t *testing.T) {
	path := writeTemp(t, "runtime", elfHeader(0x3E)) // x86-64

	errs := validateRuntimeBinary(path)
	if len(errs) == 0 {
		t.Fatal("an x86-64 binary must be rejected; AgentCore runs arm64")
	}
	if !strings.Contains(errs[0], "x86-64") {
		t.Errorf("error %q should name the architecture found", errs[0])
	}
	if !strings.Contains(errs[0], "build-runtime-arm64") {
		t.Errorf("error %q should say how to fix it", errs[0])
	}
}

func TestValidateRuntimeBinary_RejectsANonELF(t *testing.T) {
	path := writeTemp(t, "runtime", []byte("#!/bin/sh\necho hello\n"))

	errs := validateRuntimeBinary(path)
	if len(errs) == 0 {
		t.Fatal("a shell script is not a runtime binary")
	}
	if !strings.Contains(errs[0], "not a Linux executable") {
		t.Errorf("error %q should say what is wrong", errs[0])
	}
}

// Config validation runs where the artifact need not exist — validating before
// the build step, or on a machine that only holds the config. Failing there
// would make a legitimate config impossible to validate, and apply reports a
// missing binary on its own.
func TestValidateRuntimeBinary_IgnoresAMissingFile(t *testing.T) {
	if errs := validateRuntimeBinary("/no/such/runtime/binary"); len(errs) != 0 {
		t.Errorf("validateRuntimeBinary = %v, want no errors for a path that does not exist", errs)
	}
}

func TestValidateRuntimeBinary_IgnoresAnUnsetPath(t *testing.T) {
	if errs := validateRuntimeBinary(""); len(errs) != 0 {
		t.Errorf("validateRuntimeBinary = %v, want no errors when unset", errs)
	}
}

// A file too short to hold a header is malformed, not merely absent.
func TestValidateRuntimeBinary_RejectsATruncatedFile(t *testing.T) {
	path := writeTemp(t, "runtime", []byte("\x7fELF"))

	if errs := validateRuntimeBinary(path); len(errs) == 0 {
		t.Error("a truncated file must be rejected")
	}
}
