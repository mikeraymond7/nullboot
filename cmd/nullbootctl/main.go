// This file is part of nullboot
// Copyright 2021 Canonical Ltd.
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"flag"
	"log"
	"os"
	"sort"

	efi "github.com/canonical/go-efilib"
	efi_linux "github.com/canonical/go-efilib/linux"
	"github.com/canonical/nullboot/efibootmgr"
)

var noTPM = flag.Bool("no-tpm", false, "Do not do any resealing with the TPM")
var noEfivars = flag.Bool("no-efivars", false, "Do not use or update the EFI variables")
var outputJSON = flag.String("output-json", "", "JSON file to write (also disables writing real EFI variables)")

func main() {
	var assets *efibootmgr.TrustedAssets
	var err error
	flag.Parse()

	const (
		esp             = "/boot/efi"
		shimSourceDir   = "/usr/lib/nullboot/shim"
		kernelSourceDir = "/usr/lib/linux/efi"
		vendor          = "ubuntu"
	)

	// FIXME: Let's actually add some arg parsing and stuff?
	if !*noTPM {
		assets, err = efibootmgr.ReadTrustedAssets()
		if err != nil {
			log.Println("cannot read trusted asset hashes:", err)
			os.Exit(1)
		}

		for _, p := range []string{shimSourceDir, kernelSourceDir} {
			if err := assets.TrustNewFromDir(p); err != nil {
				log.Println("cannot add new assets from", p, ":", err)
				os.Exit(1)
			}
		}

		if err := efibootmgr.TrustCurrentBoot(assets, esp); err != nil {
			log.Println("cannot trust boot assets used for current boot:", err)
			os.Exit(1)
		}
	}

	var efivars efibootmgr.EFIVariables
	if *outputJSON != "" || *noEfivars {
		efivars = &efibootmgr.MockEFIVariables{}
	} else {
		efivars = efibootmgr.RealEFIVariables{}
	}

	if !efibootmgr.VariablesSupported(efivars) {
		log.Println("EFI Variables are not supported")
		os.Exit(1)
	}

	km, err := efibootmgr.NewKernelManager(esp, kernelSourceDir, vendor)
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}

	if assets != nil {
		if err := assets.Save(); err != nil {
			log.Println("cannot update list of trusted boot assets:", err)
			os.Exit(1)
		}

		// Initial reseal against new assets
		if err := efibootmgr.ResealKey(assets, km, esp, shimSourceDir, vendor); err != nil {
			log.Println("initial reseal failed:", err)
			os.Exit(1)
		}
	}

	// Install the shim
	updatedShim, err := efibootmgr.InstallShim(esp, shimSourceDir, vendor)
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
	if updatedShim {
		log.Print("Updated shim")
	}
	// Install new kernels and commit to bootloader config. This
	// way
	targetKernels, err := km.InstallSourceKernels()
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}

	// Sorting ensures that the bootEntries will be sorted in version order
	// which eventually leads to BootOrder being in version order
	sort.Slice(targetKernels, func(i, j int) bool {
		a := targetKernels[i].Version
		b := targetKernels[j].Version
		return a.GreaterThan(b)
	})
	kernelBootEntries := km.GenerateBootEntries(targetKernels)
	// In case something goes awry, this can help recover users on reboot
	// Note the order of bootEntries determines shim fallback boot order
	km.WriteShimFallback(kernelBootEntries)

	EfiBootEntryNames, err := efibootmgr.GetVariableNames(efivars, efi.GlobalVariable)
	if err != nil {
		log.Printf("Error determining EFI Boot Variable Names: %v", err)
	}
	entries := []BootEntry
	for _, bootName := range EfiBootEntryNames {

	}
	bootOrder, bootOrderAttrs, err := efivars.GetVariable(efi.GlobalVariable, "BootOrder")
	if err != nil {
		log.Print("Unable to get 'BootOrder': %v", err)
		os.Exit(1)
	}

	// 1. Gather all BootXXXX entries
	// 2. Create BootXXXX list
	// If BootEntry corresponds to existing BootXXXX entry append to list
	// Else create new internal BootXXXX and append to list
	// Repeat for all BootEntry
	// 3. Gather BootOrder
	// 4. Create new BootOrder with internal BootXXXX entries, not adding
	// BootXXXX twice (i.e. removing any existing BootXXXX from BootOrder
	// that are already in our internal list)
	// 5. Determine which BootXXXX entries are obsolete (they won't be in
	// the BootOrder or internal BootXXXX list)
	// 6. Write all internal BootXXXX that aren't already on the system
	// 7. Write new BootOrder on the system
	// 8. Delete obsolete BootXXXX entries
	// 9. Re-measure/re-seal

	// Cleanup old entries
	kernelPaths, err := km.FindObsoleteKernelPaths()
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
	if err = km.CommitToBootLoader(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
	if err = km.CommitToBootLoader(); err != nil {
		log.Print(err)
		os.Exit(1)
	}

	if assets != nil {
		assets.RemoveObsolete()
		if err := assets.Save(); err != nil {
			log.Println("cannot update list of trusted boot assets:", err)
			os.Exit(1)
		}

		// Final reseal to remove obsolete assets from profile
		if err := efibootmgr.ResealKey(assets, km, esp, shimSourceDir, vendor); err != nil {
			log.Println("final reseal failed:", err)
			os.Exit(1)
		}
	}

	if jsonEfivars, ok := efivars.(*efibootmgr.MockEFIVariables); ok {
		json, err := jsonEfivars.JSON()
		if err != nil {
			log.Println("cannot write json:", err)
			os.Exit(2)
		}

		f, err := os.Create(*outputJSON)
		if err != nil {
			log.Printf("Could not open JSON output file %s: %v", *outputJSON, err)
			os.Exit(1)
		}
		defer f.Close()

		_, err = f.Write(json)
		if err != nil {
			log.Printf("Could not write JSON output file %s: %v", *outputJSON, err)
			os.Exit(1)
		}
	}
}
