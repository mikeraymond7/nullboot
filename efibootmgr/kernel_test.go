// This file is part of nullboot
// Copyright 2021 Canonical Ltd.
// SPDX-License-Identifier: GPL-3.0-only

package efibootmgr

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"reflect"
	"strings"
	"testing"

	"github.com/canonical/go-efilib"
	"github.com/spf13/afero"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

func CheckFilesEqual(fs afero.Fs, want string, got string) error {
	wantBytes, err := afero.ReadFile(fs, want)
	if err != nil {
		return fmt.Errorf("Could not read want: %v", err)
	}
	gotBytes, err := afero.ReadFile(fs, got)
	if err != nil {
		return fmt.Errorf("Could not read got: %v", err)
	}
	if !bytes.Equal(wantBytes, gotBytes) {
		return fmt.Errorf("Expected: %v, got: %v", string(wantBytes), string(gotBytes))
	}
	return nil

}

func CreateMockFileSystem() afero.Fs {
	memFs := afero.NewMemMapFs()
	appFs = MapFS{memFs}
	afero.WriteFile(memFs, "/usr/lib/linux/kernel.efi-1.0-1-generic", []byte("1.0-1-generic"), 0644)
	afero.WriteFile(memFs, "/usr/lib/linux/kernel.efi-1.0-12-generic", []byte("1.0-12-generic"), 0644)
	afero.WriteFile(memFs, "/boot/efi/EFI/ubuntu/<dummy>", []byte(""), 0644)
	afero.WriteFile(memFs, "/boot/efi/EFI/ubuntu/shimx64.efi", []byte("file a"), 0644)

	return memFs
}
func BasicMockVars() MockEFIVariables {
	// Mock EFI variables
	mockvars := MockEFIVariables{
		map[efi.VariableDescriptor]mockEFIVariable{
			{GUID: efi.GlobalVariable, Name: "BootOrder"}:   {[]byte{1, 0, 2, 0, 3, 0}, 123},
			{GUID: efi.GlobalVariable, Name: "BootCurrent"}: {[]byte{1, 0}, 1},
			{GUID: efi.GlobalVariable, Name: "Boot0000"}:    {UsbrBootCdromOptBytes, 42},
			{GUID: efi.GlobalVariable, Name: "Boot0001"}:    {UsbrBootCdromOptBytes, 43},
			{GUID: efi.GlobalVariable, Name: "Boot0003"}:    {UsbrBootCdromOptBytes, 44},
		},
	}
	return mockvars
}

func BasicKm(mockvars MockEFIVariables) (*KernelManager, error) {

	// Mock Boot and Kernel Managers
	bm, err := NewBootManagerForVariables(&mockvars)
	if err != nil {
		err = fmt.Errorf("Could not create boot manager: %v", err)
	}
	km, err := NewKernelManager("/boot/efi", "/usr/lib/linux", "ubuntu", &bm)
	if err != nil {
		err = fmt.Errorf("Could not create kernel manager: %v", err)
	}
	return km, err
}

func CreateMockBootEntries(km *KernelManager) error {
	// Mock memory map for files that are expected to exist in
	// this fake file system
	memFs := afero.NewMemMapFs()
	appFs = MapFS{memFs}

	// Add boot entries to Kernel Manager
	for i := range 3 {
		afero.WriteFile(memFs, fmt.Sprintf("/boot/efi/EFI/ubuntu/k%d.efi", i), []byte("1.0-12-generic"), 0644)
		bootEntry := BootEntry{
			Filename:    fmt.Sprintf("k%d.efi", i),
			Label:       fmt.Sprintf("Ubuntu kernel %d", i),
			Options:     fmt.Sprintf(" \\ k%d", i),
			Description: fmt.Sprintf("Ubuntu kernel entry %d", i),
		}
		km.bootEntries = append(km.bootEntries, bootEntry)
		_, err := km.bootManager.FindOrCreateEntry(bootEntry, "/boot/efi/EFI/ubuntu")
		if err != nil {
			return fmt.Errorf("Could not create boot entry for k%d: %v", i, err)
		}
	}
	return nil
}

func TestKernelManagerNewAndInstallKernels(t *testing.T) {
	appArchitecture = "x64"
	memFs := afero.NewMemMapFs()
	appFs = MapFS{memFs}
	afero.WriteFile(memFs, "/usr/lib/linux/kernel.efi-1.0-12-generic", []byte("1.0-12-generic"), 0644)
	afero.WriteFile(memFs, "/usr/lib/linux/kernel.efi-1.0-1-generic", []byte("1.0-1-generic"), 0644)
	afero.WriteFile(memFs, "/boot/efi/EFI/ubuntu/<dummy>", []byte(""), 0644)
	afero.WriteFile(memFs, "/etc/kernel/cmdline", []byte("root=magic"), 0644)
	afero.WriteFile(memFs, "/boot/efi/EFI/ubuntu/shimx64.efi", []byte("file a"), 0644)
	mockvars := MockEFIVariables{
		map[efi.VariableDescriptor]mockEFIVariable{
			{GUID: efi.GlobalVariable, Name: "BootOrder"}: {[]byte{1, 0, 2, 0, 3, 0}, 123},
			{GUID: efi.GlobalVariable, Name: "Boot0001"}:  {UsbrBootCdromOptBytes, 42},
		},
	}

	// Create an obsolete Boot0000 entry that we want to collect at the end.
	bm, _ := NewBootManagerForVariables(&mockvars)
	if _, err := bm.FindOrCreateEntry(BootEntry{Filename: "shimx64.efi", Label: "Ubuntu with obsolete kernel", Options: ""}, "/boot/efi/EFI/ubuntu"); err != nil {
		t.Fatal(err)
	}

	km, err := NewKernelManager("/boot/efi", "/usr/lib/linux", "ubuntu", &bm)
	if err != nil {
		t.Fatalf("Could not create kernel manager: %v", err)
	}
	wantSourceKernels := []string{"kernel.efi-1.0-12-generic", "kernel.efi-1.0-1-generic"}
	if !reflect.DeepEqual(km.sourceKernels, wantSourceKernels) {
		t.Fatalf("Expected %v, got %v", wantSourceKernels, km.sourceKernels)
	}
	var wantTargetKernels []string
	if !reflect.DeepEqual(km.targetKernels, wantTargetKernels) {
		t.Fatalf("Expected %v, got %v", wantTargetKernels, km.targetKernels)
	}

	if err := km.InstallKernels(); err != nil {
		t.Errorf("Could not install kernels: %v", err)
	}

	if err := CheckFilesEqual(memFs, "/usr/lib/linux/kernel.efi-1.0-12-generic", "/boot/efi/EFI/ubuntu/kernel.efi-1.0-12-generic"); err != nil {
		t.Error(err)
	}
	if err := CheckFilesEqual(memFs, "/usr/lib/linux/kernel.efi-1.0-1-generic", "/boot/efi/EFI/ubuntu/kernel.efi-1.0-1-generic"); err != nil {
		t.Error(err)
	}

	if err := km.CommitToBootLoader(); err != nil {
		t.Errorf("Could not commit to bootloader: %v", err)
	}

	file, err := memFs.Open("/boot/efi/EFI/ubuntu/BOOT" + strings.ToUpper(GetEfiArchitecture()) + ".CSV")
	if err != nil {
		t.Fatalf("Could not open boot.csv: %v", err)
	}
	reader := transform.NewReader(file, unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder())
	data, err := ioutil.ReadAll(reader)
	if err != nil {
		t.Fatalf("Could not read boot.csv: %v", err)
	}

	want := ("shim" + GetEfiArchitecture() + ".efi,Ubuntu with kernel 1.0-1-generic,\\kernel.efi-1.0-1-generic root=magic ,Ubuntu entry for kernel 1.0-1-generic\n" +
		"shim" + GetEfiArchitecture() + ".efi,Ubuntu with kernel 1.0-12-generic,\\kernel.efi-1.0-12-generic root=magic ,Ubuntu entry for kernel 1.0-12-generic\n")
	if want != string(data) {
		t.Errorf("Boot entry mismatch:\nExpected:\n%v\nGot:\n%v", want, string(data))
	}

	// Validate we have actually written the EFI stuff we want
	bm, err = NewBootManagerForVariables(&mockvars)
	if err != nil {
		t.Fatalf("Could not create boot manager: %v", err)
	}

	// So we already had 1 populated with a foreign boot entry, this should be preserved.
	if !reflect.DeepEqual(bm.bootOrder, []int{2, 3, 1}) {
		t.Fatalf("Unexpected boot order %v", bm.bootOrder)
	}

	for i, desc := range map[int]string{2: "Ubuntu with kernel 1.0-12-generic", 3: "Ubuntu with kernel 1.0-1-generic", 1: "USBR BOOT CDROM"} {
		if bm.entries[i].LoadOption.Description != desc {
			t.Errorf("Expected boot entry %d Description %s, got %s", i, desc, bm.entries[i].LoadOption.Description)
		}

	}
}
func TestKernelManager_noCmdLine(t *testing.T) {
	appArchitecture = "x64"
	memFs := afero.NewMemMapFs()
	appFs = MapFS{memFs}
	afero.WriteFile(memFs, "/usr/lib/linux/kernel.efi-1.0-12-generic", []byte("1.0-12-generic"), 0644)
	afero.WriteFile(memFs, "/usr/lib/linux/kernel.efi-1.0-1-generic", []byte("1.0-1-generic"), 0644)
	afero.WriteFile(memFs, "/boot/efi/EFI/ubuntu/<dummy>", []byte(""), 0644)
	afero.WriteFile(memFs, "/boot/efi/EFI/ubuntu/shimx64.efi", []byte("file a"), 0644)
	mockvars := MockEFIVariables{
		map[efi.VariableDescriptor]mockEFIVariable{
			{GUID: efi.GlobalVariable, Name: "BootOrder"}: {[]byte{1, 0, 2, 0, 3, 0}, 123},
			{GUID: efi.GlobalVariable, Name: "Boot0001"}:  {UsbrBootCdromOptBytes, 42},
		},
	}

	// Create an obsolete Boot0000 entry that we want to collect at the end.
	bm, _ := NewBootManagerForVariables(&mockvars)
	if _, err := bm.FindOrCreateEntry(BootEntry{Filename: "shimx64.efi", Label: "Ubuntu with obsolete kernel", Options: ""}, "/boot/efi/EFI/ubuntu"); err != nil {
		t.Fatal(err)
	}

	km, err := NewKernelManager("/boot/efi", "/usr/lib/linux", "ubuntu", &bm)
	if err := km.InstallKernels(); err != nil {
		t.Errorf("Could not install kernels: %v", err)
	}

	if err := km.CommitToBootLoader(); err != nil {
		t.Errorf("Could not commit to bootloader: %v", err)
	}

	file, err := memFs.Open("/boot/efi/EFI/ubuntu/BOOT" + strings.ToUpper(GetEfiArchitecture()) + ".CSV")
	if err != nil {
		t.Fatalf("Could not open boot.csv: %v", err)
	}
	reader := transform.NewReader(file, unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder())
	data, err := ioutil.ReadAll(reader)
	if err != nil {
		t.Fatalf("Could not read boot.csv: %v", err)
	}

	want := ("shim" + GetEfiArchitecture() + ".efi,Ubuntu with kernel 1.0-1-generic,\\kernel.efi-1.0-1-generic ,Ubuntu entry for kernel 1.0-1-generic\n" +
		"shim" + GetEfiArchitecture() + ".efi,Ubuntu with kernel 1.0-12-generic,\\kernel.efi-1.0-12-generic ,Ubuntu entry for kernel 1.0-12-generic\n")
	if want != string(data) {
		t.Errorf("Boot entry mismatch:\nExpected:\n%v\nGot:\n%v", want, string(data))
	}

	// Validate we have actually written the EFI stuff we want
	bm, err = NewBootManagerForVariables(&mockvars)
	if err != nil {
		t.Fatalf("Could not create boot manager: %v", err)
	}

	// So we already had 1 populated with a foreign boot entry, this should be preserved.
	if !reflect.DeepEqual(bm.bootOrder, []int{2, 3, 1}) {
		t.Fatalf("Unexpected boot order %v", bm.bootOrder)
	}

	for i, desc := range map[int]string{2: "Ubuntu with kernel 1.0-12-generic", 3: "Ubuntu with kernel 1.0-1-generic", 1: "USBR BOOT CDROM"} {
		if bm.entries[i].LoadOption.Description != desc {
			t.Errorf("Expected boot entry %d Description %s, got %s", i, desc, bm.entries[i].LoadOption.Description)
		}

	}
}

func TestKernelManagerRemoveObsoleteKernels(t *testing.T) {
	appArchitecture = "x64"
	memFs := afero.NewMemMapFs()
	appFs = MapFS{memFs}
	afero.WriteFile(memFs, "/usr/lib/linux/kernel.efi-1.0-12-generic", []byte("1.0-12-generic"), 0644)
	afero.WriteFile(memFs, "/boot/efi/EFI/ubuntu/kernel.efi-1.0-12-generic", []byte("1.0-12-generic"), 0644)
	afero.WriteFile(memFs, "/boot/efi/EFI/ubuntu/kernel.efi-1.0-1-generic", []byte("1.0-1-generic"), 0644)
	afero.WriteFile(memFs, "/boot/efi/EFI/ubuntu/BOOTX64.CSV", []byte(""), 0644)
	afero.WriteFile(memFs, "/etc/kernel/cmdline", []byte("root=magic"), 0644)
	mockvars := MockEFIVariables{
		map[efi.VariableDescriptor]mockEFIVariable{
			{GUID: efi.GlobalVariable, Name: "BootOrder"}: {[]byte{}, 123},
		},
	}
	bm, err := NewBootManagerForVariables(&mockvars)
	if err != nil {
		t.Fatalf("Could not create boot manager: %v", err)
	}
	km, err := NewKernelManager("/boot/efi", "/usr/lib/linux", "ubuntu", &bm)
	if err != nil {
		t.Fatalf("Could not create kernel manager: %v", err)
	}
	if err := km.RemoveObsoleteKernels(); err != nil {
		t.Errorf("Failed to remove obsolete kernels: %v", err)
	}

	if _, err := memFs.Stat("/boot/efi/EFI/ubuntu/kernel.efi-1.0-12-generic"); err != nil {
		t.Errorf("missing file: %v\n", err)
	}
	if _, err := memFs.Stat("/boot/efi/EFI/ubuntu/kernel.efi-1.0-1-generic"); err == nil {
		t.Errorf("did not expect obsolete kernel to be present")
	}

	if km.targetKernels != nil {
		t.Errorf("expected list of target kernels to be empty now, got: %v", km.targetKernels)
	}

}

func TestKernelManagerSetKernelFallback(t *testing.T) {
	mockvars := BasicMockVars()
	km, err := BasicKm(mockvars)
	if err != nil {
		t.Fatalf("Unable to construct basic KernelManager: %v", err)
	}

	if err = CreateMockBootEntries(km); err != nil {
		t.Fatalf("Unable to create mock boot entries: %v", err)
	}

	// Function we are testing
	if err := km.SetKernelFallback(); err != nil {
		t.Errorf("Unable to set kernel fallback mechanism: %v", err)
	}

	// Check BootNext is set correctly
	// Boot0002 will be the first created in CreateMockBootEntries
	expectedInternalBootNext := 2
	expectedSystemBootNext := []byte{2, 0}
	systemBootNext := mockvars.store[efi.VariableDescriptor{GUID: efi.GlobalVariable, Name: "BootNext"}].data
	if !bytes.Equal(mockvars.store[efi.VariableDescriptor{GUID: efi.GlobalVariable, Name: "BootNext"}].data, expectedSystemBootNext) {
		t.Errorf("System BootNext is not correct, expected: %v, got: %v", expectedSystemBootNext, systemBootNext)
	}
	if km.bootManager.bootNext != expectedInternalBootNext {
		t.Errorf("Internal BootNext is not correct, expected: %v, got: %v", expectedInternalBootNext, km.bootManager.bootNext)
	}

	// Check BootOrder is not changed
	expectedInternalBootOrder := []int{1, 2, 3}
	expectedSystemBootOrder := []byte{1, 0, 2, 0, 3, 0}

	if !reflect.DeepEqual(km.bootManager.bootOrder, expectedInternalBootOrder) {
		t.Errorf("Internal BootOrder was unexpectedly modified, expected: %v, got: %v", expectedInternalBootOrder, km.bootManager.bootOrder)
	}
	systemBootOrder := mockvars.store[efi.VariableDescriptor{GUID: efi.GlobalVariable, Name: "BootOrder"}].data
	if !bytes.Equal(systemBootOrder, expectedSystemBootOrder) {
		t.Errorf("System BootOrder was unexpectedly modified, expected: %v, got: %v", expectedSystemBootOrder, systemBootOrder)
	}
}

func TestKernelManagerBootLoaderNeedsUpdate(t *testing.T) {
	// Create a fake filesytem and override read/write operations
	CreateMockFileSystem()

	// Mock EFI variables
	mockvars := BasicMockVars()

	// Mock Boot and Kernel Managers
	km, err := BasicKm(mockvars)
	if err != nil {
		t.Fatalf("Unable to construct basic KernelManager: %v", err)
	}

	// Populate some misc boot entries
	CreateMockBootEntries(km)

	km.bootManager.bootCurrent = 2
	if needsUpdate, err := km.BootLoaderNeedsUpdate(); needsUpdate != true || err != nil {
		t.Errorf("Expected true, nil but got: %v, %v", needsUpdate, err)
	}
}
