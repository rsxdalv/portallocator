package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

const AllocationsPath = "/etc/port_allocator/allocations.json"

// Allocations is the on-disk cache. Terraform state is authoritative.
type Allocations struct {
	Version     int            `json:"version"`
	ManagedBy   string         `json:"managed_by"`
	Allocations map[string]int `json:"allocations"`
}

func newAllocations() Allocations {
	return Allocations{
		Version:     1,
		ManagedBy:   "port_allocator",
		Allocations: map[string]int{},
	}
}

func readAllocations() (Allocations, diag.Diagnostic) {
	a := newAllocations()
	raw, err := os.ReadFile(AllocationsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return a, nil
		}
		return a, diag.NewErrorDiagnostic(
			"read allocations.json",
			fmt.Sprintf("%s: %v", AllocationsPath, err),
		)
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, diag.NewErrorDiagnostic(
			"parse allocations.json",
			fmt.Sprintf("%s: %v", AllocationsPath, err),
		)
	}
	if a.Allocations == nil {
		a.Allocations = map[string]int{}
	}
	return a, nil
}

// writeAllocations persists atomically via tmp+rename.
func writeAllocations(a Allocations) diag.Diagnostic {
	if err := os.MkdirAll("/etc/port_allocator", 0755); err != nil {
		return diag.NewErrorDiagnostic("mkdir /etc/port_allocator", err.Error())
	}
	body, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return diag.NewErrorDiagnostic("marshal allocations", err.Error())
	}
	tmp := AllocationsPath + ".tmp"
	if err := os.WriteFile(tmp, body, 0644); err != nil {
		return diag.NewErrorDiagnostic("write tmp", err.Error())
	}
	if err := os.Rename(tmp, AllocationsPath); err != nil {
		return diag.NewErrorDiagnostic("rename", err.Error())
	}
	return nil
}

// rangeAllocator picks the lowest free port in [start, end] from the cache.
type rangeAllocator struct {
	start, end int
}

func (a rangeAllocator) next() (int, error) {
	used := map[int]bool{}
	allocs, d := readAllocations()
	if d != nil {
		return 0, fmt.Errorf("allocator: %s", d.Summary())
	}
	for _, p := range allocs.Allocations {
		used[p] = true
	}
	for p := a.start; p <= a.end; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free port in [%d,%d]", a.start, a.end)
}
