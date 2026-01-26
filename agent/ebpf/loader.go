// Package ebpf provides eBPF program loading and management for the InfraLens agent.
package ebpf

import (
	"errors"
	"fmt"

	"github.com/cilium/ebpf"
)

// Objects contains all loaded BPF objects (programs and maps).
type Objects struct {
	bpfObjects
}

// EventT is the event structure received from the BPF program.
// This is an alias to the generated bpfEventT type.
type EventT = bpfEventT

// LoadObjects loads the compiled BPF program and maps into the kernel.
func LoadObjects() (*Objects, error) {
	var objs bpfObjects
	if err := loadBpfObjects(&objs, nil); err != nil {
		var ve *ebpf.VerifierError
		if errors.As(err, &ve) {
			return nil, fmt.Errorf("verifier error: %+v", ve)
		}
		return nil, fmt.Errorf("loading objects: %w", err)
	}
	return &Objects{objs}, nil
}

// Close releases all resources associated with the BPF objects.
func (o *Objects) Close() error {
	return o.bpfObjects.Close()
}
