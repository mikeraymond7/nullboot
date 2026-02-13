// This file is part of nullboot
// Copyright 2021 Canonical Ltd.
// SPDX-License-Identifier: GPL-3.0-only

package efibootmgr

import (
	"fmt"
	"github.com/knqyf263/go-deb-version"
	"io"
	"log"
	"path"
	"sort"
	"strings"
)

const (
	kernelPrefix    = "kernel.efi-"
	kernelPrefixLen = len(kernelPrefix)
)

type Kernel struct {
	Version  version.Version
	Filename string
}

type KernelManager struct {
	sourceDir     string
	targetDir     string
	kernelOptions string
	vendor        string
}

// NewKernelManager returns a new kernel manager managing kernels in the host system
func NewKernelManager(esp string, sourceDir string, vendor string) (*KernelManager, error) {
	var km KernelManager

	km.sourceDir = sourceDir
	km.targetDir = path.Join(esp, "EFI", vendor)
	km.vendor = vendor

	if file, err := appFs.Open("/etc/kernel/cmdline"); err == nil {
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			return nil, fmt.Errorf("Cannot read kernel command line: %w", err)
		}

		km.kernelOptions = strings.TrimSpace(string(data))
	}

	return &km, nil
}

// Copies any new or updated kernels in the sourceDir to the targetDir
// Returns the source kernels, sans any kernels that could not be updated
// or installed.
func (km *KernelManager) InstallSourceKernels() ([]Kernel, error) {
	sourceKernels, err := km.GetSourceKernels()
	if err != nil {
		return []Kernel{}, fmt.Errorf("unable to get source kernels from %s: %w", km.sourceDir, err)
	}

	// This will contain source kernels that don't error in MaybeUpdateFile
	var targetKernels []Kernel
	for _, sk := range sourceKernels {
		sourceKernelPath := path.Join(km.sourceDir, sk.Filename)
		targetKernelPath := path.Join(km.targetDir, sk.Filename)
		updated, err := MaybeUpdateFile(targetKernelPath, sourceKernelPath)
		if err != nil {
			log.Printf("Could not install kernel %s: %v", sk.Filename, err)
			continue
		}
		if updated {
			log.Printf("Installed or updated kernel %s", sk.Filename)
		}
		targetKernels = append(targetKernels, sk)
	}
	return targetKernels, nil
}

// Collect all Kernel EFIs in the sourceDir
func (km *KernelManager) GetSourceKernels() ([]Kernel, error) {
	return readKernels(km.sourceDir)
}

// Collect all Kernel EFIs in the targetDir
func (km *KernelManager) GetTargetKernels() ([]Kernel, error) {
	return readKernels(km.targetDir)
}

func (km *KernelManager) FindObsoleteKernelPaths() ([]string, error) {
	sourceKernels, err := km.GetSourceKernels()
	if err != nil {
		return []string{}, fmt.Errorf("unable to get source kernels from %s: %w", km.sourceDir, err)
	}
	targetKernels, err := km.GetTargetKernels()
	if err != nil {
		return []string{}, fmt.Errorf("unable to get target kernels from %s: %w", km.targetDir, err)
	}

	obsoleteKernelPaths := []string{}
	for _, tk := range targetKernels {
		isObsolete := true
		for _, sk := range sourceKernels {
			if tk == sk {
				isObsolete = false
				break
			}
		}
		if isObsolete {
			obsoletePath := path.Join(km.targetDir, tk.Filename)
			obsoleteKernelPaths = append(obsoleteKernelPaths, obsoletePath)
		}
	}
	return obsoleteKernelPaths, nil
}

func (km *KernelManager) GenerateBootEntries(kernels []Kernel) []BootEntry {
	bootEntries := []BootEntry{}
	for _, k := range kernels {
		bootEntry := NewBootEntry(km.kernelOptions, k)
		bootEntries = append(bootEntries, bootEntry)
	}
	return bootEntries
}

func (km *KernelManager) WriteShimFallback(bootEntries []BootEntry) {
	// We completely own the shim fallback file, so just write it
	if err := WriteShimFallbackToFile(path.Join(km.targetDir, "BOOT"+strings.ToUpper(GetEfiArchitecture())+".CSV"), bootEntries); err != nil {
		log.Printf("Failed to configure shim fallback loader: %v", err)
	}
}

// readKernels returns a list of all kernel EFIs in the specified directory
func readKernels(dir string) ([]Kernel, error) {
	var kernels []Kernel
	entries, err := appFs.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("Could not determine kernels: %w", err)
	}
	for _, e := range entries {
		if kernelVersionStr, err := myGetKernelABI(e.Name()); err == nil {
			v, err := version.NewVersion(kernelVersionStr)
			if err != nil {
				return []Kernel{}, fmt.Errorf("unable to parse kernel version of %s: %w", e.Name(), err)
			}
			kernel := Kernel{v, e.Name()}
			kernels = append(kernels, kernel)
		}
	}
	// Sort descending
	sort.Slice(kernels, func(i, j int) bool {
		a := kernels[i].Version
		b := kernels[j].Version
		return a.GreaterThan(b)
	})
	return kernels, err
}

// getKernelABI returns the kernel ABI part of the kernel filename
func myGetKernelABI(kernelName string) (string, error) {
	if strings.HasPrefix(kernelName, kernelPrefix) {
		return kernelName[kernelPrefixLen:], nil
	}
	return "", fmt.Errorf("unable to parse version from kernel name: %s", kernelName)

}
