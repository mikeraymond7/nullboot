// This file is part of nullboot
// Copyright 2021 Canonical Ltd.
// SPDX-License-Identifier: GPL-3.0-only

package efibootmgr

import (
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"sort"
	"strings"

	"github.com/knqyf263/go-deb-version"
)

const (
	kernelPrefix    = "kernel.efi-"
	kernelPrefixLen = len(kernelPrefix)
)

type KernelEntry struct {
	kernel Kernel
	entry  BootEntry
}

type VersionParsingError struct {
	Message string
	Err     error
}

func (e VersionParsingError) Error() string {
	return e.Message
}

func (e VersionParsingError) Unwrap() error {
	return e.Err
}

type Kernel struct {
	Version  version.Version
	FilePath string
}

func (k *Kernel) GetKernelName() string {
	return path.Base(k.FilePath)
}

func NewKernel(kernelPath string) (Kernel, error) {
	kernelName := path.Base(kernelPath)
	if versionStr, err := getKernelABI(kernelName); err == nil {
		v, err := version.NewVersion(versionStr)
		if err != nil {
			err = fmt.Errorf("could not parse kernel version of %s: %w", kernelName, err)
			return Kernel{}, VersionParsingError{Message: err.Error(), Err: err}
		}
		return Kernel{v, kernelPath}, nil
	}
	return Kernel{}, fmt.Errorf("unrecognized kernel naming format: %s", kernelName)
}

type UEFIBootAsset struct {
	entry    BootEntry
	entryVar BootEntryVariable
}

// KernelManager manages kernels in an SP vendor directory.
//
// It will update or install shim, copy in any new kernels,
// remove old kernels, and configure boot in shim and BDS.
type KernelManager struct {
	sourceDir     string // sourceDir is the location to copy kernels from
	targetDir     string // targetDir is a vendor directory on the ESP
	kernelOptions string // options to pass to kernel
}

// NewKernelManager returns a new kernel manager managing kernels in the host system
func NewKernelManager(esp, sourceDir, vendor string) (*KernelManager, error) {
	var km KernelManager

	km.sourceDir = sourceDir
	km.targetDir = path.Join(esp, "EFI", vendor)

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

func (km *KernelManager) GetSourceKernels() ([]Kernel, error) {
	return km.readKernels(km.sourceDir)
}

func (km *KernelManager) GetTargetKernels() ([]Kernel, error) {
	return km.readKernels(km.targetDir)
}

// readKernels returns a list of all kernels in the
func (km *KernelManager) readKernels(dir string) ([]Kernel, error) {
	var kernels []Kernel
	entries, err := appFs.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("Could not determine kernels: %w", err)
	}
	for _, e := range entries {
		kernel, err := NewKernel(path.Join(dir, e.Name()))
		if err != nil {
			var vpe *VersionParsingError
			// This means the kernel starts with "kernel.efi-" but there is
			// a problem with the version sequence after that string
			if errors.As(err, &vpe) {
				return []Kernel{}, err
			}
			// This is likely an item in sourceDir that isn't a kernel
			continue
		}
		kernels = append(kernels, kernel)
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
func getKernelABI(kernel string) (string, error) {
	if strings.HasPrefix(kernel, kernelPrefix) {
		return kernel[kernelPrefixLen:], nil
	}
	return "", fmt.Errorf("unknown naming format of kernel %s, unable to parse ABI", kernel)
}

// Installs kernels to the ESP
func (km *KernelManager) InstallKernels(kernels []Kernel) ([]Kernel, error) {
	tgtKernels := []Kernel{}
	sourceKernels, err := km.GetSourceKernels()
	if err != nil {
		return []Kernel{}, fmt.Errorf("unable to get source kernels: %w", err)
	}
	for _, sk := range sourceKernels {
		kName := sk.GetKernelName()
		tgtPath := path.Join(km.targetDir, kName)
		updated, err := MaybeUpdateFile(tgtPath, sk.FilePath)
		if err != nil {
			log.Printf("Could not install kernel %s: %v", kName, err)
			continue
		}
		if updated {
			log.Printf("Installed or updated kernel %s", kName)
		}
		tgtKernel, err := NewKernel(tgtPath)
		tgtKernels = append(tgtKernels, tgtKernel)
	}

	return tgtKernels, nil
}

// RegisterNewKernelEFIs creates EFI variables for each new kernel and
// registers them on the host machine.
func RegisterKernelEFIs(kernelEntries []KernelEntry) error {
	for _, k := range kernelEntries {
		relativeDir := path.Dir(k.kernel.FilePath)
		if _, err := FindOrCreateEntry(k.entry, relativeDir); err != nil {
			return fmt.Errorf("unable to create EFI Boot Entry for %s: %w", k.kernel.FilePath, err)
		}
	}
	return nil
}

// RemoveObsoleteKernels removes old kernels in the ESP vendor directory
func (km *KernelManager) RemoveObsoleteKernels() error {
	sourceKernels, err := km.GetSourceKernels()
	if err != nil {
		return fmt.Errorf("unable to get source kernels: %w", err)
	}
	targetKernels, err := km.GetTargetKernels()
	if err != nil {
		return fmt.Errorf("unable to get target kernels: %w", err)
	}

	for _, tk := range targetKernels {
		// Only kernels with a source and target are kept
		for _, sk := range sourceKernels {
			if sk == tk {
				continue
			}
		}

		if err := appFs.Remove(path.Join(km.targetDir, tk.filename)); err != nil {
			log.Printf("Could not remove kernel %s: %v", tk.filename, err)
			continue
		}

		log.Printf("Removed kernel %s", tk)
	}

	return nil
}

// CommitToBootLoader updates the firmware BDS entries and shim's boot.csv
func (km *KernelManager) CommitToBootLoader() error {
	log.Print("Configuring shim fallback loader")

	// We completely own the shim fallback file, so just write it
	bootEntries := []BootEntry{}
	for _, kernelEntry := range km.kernelEntries {
		bootEntries = append(bootEntries, kernelEntry.entry)
	}
	if err := WriteShimFallbackToFile(path.Join(km.targetDir, "BOOT"+strings.ToUpper(GetEfiArchitecture())+".CSV"), bootEntries); err != nil {
		log.Printf("Failed to configure shim fallback loader: %v", err)
	}

	if km.bootManager == nil {
		return nil
	}

	log.Print("Configuring UEFI boot device selection")

	// This will become the head of the new boot order
	var ourBootOrder []int

	// Add new entries, find existing ones and build target boot order
	for _, kernelEntry := range km.kernelEntries {
		entryVar, err := km.bootManager.FindBootEntryVariable(kernelEntry.entry, km.targetDir)
		if err != nil {
			return fmt.Errorf("failure to find boot entry for %s: %w", entry.Label, err)
		}
		ourBootOrder = append(ourBootOrder, entryVar.BootNumber)
	}

	// Delete any obsolete kernels
	for _, ev := range km.bootManager.entries {
		if !strings.HasPrefix(ev.LoadOption.Description, "Ubuntu ") {
			continue
		}
		isObsolete := true
		for _, num := range ourBootOrder {
			if num == ev.BootNumber {
				isObsolete = false
			}
		}
		if !isObsolete {
			continue
		}

		if err := km.bootManager.DeleteEntry(ev.BootNumber); err != nil {
			log.Printf("Could not delete Boot%04X: %v", ev.BootNumber, err)
		}
	}

	// Set the boot order
	if err := km.bootManager.PrependAndSetBootOrder(ourBootOrder); err != nil {
		return fmt.Errorf("Could not set boot order: %w", err)
	}

	return nil
}

// SetLatestKernelToBootNext sets the latest kernel to be BootNext.
//
// Returns an error if the entry does not yet exist as a BootEntryVariable
// or if there is an error setting BootNext.
func (km *KernelManager) SetLatestKernelToBootNext() error {
	latestKernel, err := km.GetLatestKernelEntry()
	if err != nil {
		return fmt.Errorf("unable to get latest kernel entry: %w", err)
	}
	latestKernelEntry := latestKernel.entry
	latestKernelEntryVar, err := km.bootManager.FindBootEntryVariable(latestKernelEntry, km.targetDir)
	if err != nil {
		return fmt.Errorf("unable to find boot variable for %s, %v: %w", latestKernelEntry.Label, latestKernelEntry.Options, err)
	}
	if err := km.bootManager.SetBootNext(latestKernelEntryVar.BootNumber); err != nil {
		return fmt.Errorf("unable to set BootNext to Boot%04X (%s): %w", latestKernelEntryVar.BootNumber, latestKernelEntry.Label, err)
	}

	return nil
}

func (km *KernelManager) IsCurrentBootLatest() (bool, error) {
	if len(km.kernelEntries) == 0 {
		return false, fmt.Errorf("no Ubuntu Kernel EFIs have been loaded")
	}

	latestKernel, err := km.GetLatestKernelEntry()
	latestKernelEntry := latestKernel.entry
	if err != nil {
		return false, fmt.Errorf("unable to get latest kernel entry: %w", err)
	}
	latestKernelEntryVar, err := km.bootManager.FindBootEntryVariable(latestKernelEntry, km.targetDir)
	if err != nil {
		return false, fmt.Errorf("unable to find latest kernel boot variable: %w", err)
	}

	// Determine if the BootEntryVariable is the BootCurrent variable
	if latestKernelEntryVar.BootNumber == km.bootManager.bootCurrent {
		return true, nil
	} else {
		return false, nil
	}
}

func (km *KernelManager) GetLatestKernelEntry() (KernelEntry, error) {
	// NOTE: Since readKernels enforces sorting, this is overkill
	// However, this is an extremely important part of the nullboot
	// fallback mechanism and is worth a bit of duplication
	if len(km.kernelEntries) == 0 {
		return KernelEntry{}, fmt.Errorf("no kernels have been registered to the KernelManager")
	} else if len(km.kernelEntries) == 1 {
		return km.kernelEntries[0], nil
	}

	curMaxIdx := 0
	curMaxVersion := km.kernelEntries[0].kernel.version
	for i := range km.kernelEntries[1:] {
		curVersion := km.kernelEntries[i].kernel.version
		if curVersion.GreaterThan(curMaxVersion) {
			curMaxVersion = curVersion
			curMaxIdx = i
		}
	}
	return km.kernelEntries[curMaxIdx], nil
}
