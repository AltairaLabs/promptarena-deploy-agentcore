package agentcore

import (
	"encoding/binary"
	"fmt"
	"os"
)

// AgentCore runs code deployments on arm64. A binary built for anything else
// uploads, deploys and reports success, then fails to exec inside the runtime —
// surfacing minutes later as a runtime that never becomes READY, with nothing
// pointing at the cause.
//
// The ELF header carries the answer in two bytes, so checking is cheap and the
// alternative is a support conversation.
const (
	elfMagic        = "\x7fELF"
	elfMachineARM64 = 0xB7
	// elfMachineOffset is where e_machine sits in a 64-bit little-endian ELF
	// header: 16 bytes of e_ident, then 2 for e_type.
	elfMachineOffset = 18
	elfHeaderMinLen  = 20
)

// elfMachineNames maps the machine types worth naming in an error. Anything
// else is reported by number, which is still enough to act on.
var elfMachineNames = map[uint16]string{
	0x03:            "x86",
	0x28:            "arm",
	0x3E:            "x86-64",
	elfMachineARM64: "arm64",
}

// validateRuntimeBinary checks the binary is an arm64 ELF, which is what
// AgentCore can run.
//
// A path that cannot be read is NOT an error here. Config validation runs in
// places the artifact need not exist yet — validating a config before the build
// step, or on a machine that only holds the config — and failing there would
// make a legitimate check impossible to pass. Apply reports a missing binary
// on its own, at the point it actually needs one.
//
// So this only speaks up about a file it can actually read, which is exactly
// the case where it has something to say.
func validateRuntimeBinary(path string) []string {
	if path == "" {
		return nil
	}

	// Same path codedeploy.go opens to upload the binary, from the same
	// trusted config; reading it here only reads it earlier.
	f, err := os.Open(path) //nolint:gosec // path is from trusted config
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	header := make([]byte, elfHeaderMinLen)
	if _, err := f.Read(header); err != nil {
		return []string{fmt.Sprintf(
			"runtime_binary_path %q is too small to be a runtime binary", path)}
	}
	if string(header[:len(elfMagic)]) != elfMagic {
		return []string{fmt.Sprintf(
			"runtime_binary_path %q is not a Linux executable; AgentCore runs a "+
				"cross-compiled ELF binary, built with `make build-runtime-arm64`", path)}
	}

	machine := binary.LittleEndian.Uint16(header[elfMachineOffset:])
	if machine == elfMachineARM64 {
		return nil
	}
	return []string{fmt.Sprintf(
		"runtime_binary_path %q is built for %s; AgentCore runs arm64, and another "+
			"architecture deploys without error and then fails to start. Build with "+
			"`make build-runtime-arm64`",
		path, machineName(machine))}
}

// machineName renders an ELF machine type for an error message.
func machineName(machine uint16) string {
	if name, ok := elfMachineNames[machine]; ok {
		return name
	}
	return fmt.Sprintf("ELF machine 0x%X", machine)
}
