// This file is part of nullboot
// Copyright 2021 Canonical Ltd.
// SPDX-License-Identifier: GPL-3.0-only

package main

import "github.com/canonical/nullboot/efibootmgr"
import "flag"
import "log"
import "os"

var confirmKernelBoot = flag.Bool("confirm-kernel-boot", false, "Ensures BootCurrent refers to the latest kernel; if BootCurrent is not yet the first entry of BootOrder, amends the boot loader; otherwise, exits for safety. Intended for use on start-up after kernel installations")
var trustKernelInstalls = flag.Bool("trust-kernel-installs", false, "Automatically adds a kernel to the boot loader and deletes obsolete boot entries; otherwise, boot loader is not updated on kernel installations until the latest kernel successfully boots via BootNext")
var noTPM = flag.Bool("no-tpm", false, "Do not do any resealing with the TPM")
var noEfivars = flag.Bool("no-efivars", false, "Do not use or update the EFI variables. Disables kernel fallback mechanism")
var outputJSON = flag.String("output-json", "", "JSON file to write. Disables writing real EFI variables and enablement of the kernel fallback mechanism")

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

	usingRealEFIVars := *outputJSON == "" && !*noEfivars
	if *confirmKernelBoot {
		if !usingRealEFIVars {
			log.Println("One of the selected flags disables reading of system EFI variables; cannot confirm the status of BootCurrent if system EFI variables are not used")
			os.Exit(1)
		}
	}

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

	var maybeBm *efibootmgr.BootManager
	var efivars efibootmgr.EFIVariables
	if *outputJSON != "" {
		efivars = &efibootmgr.MockEFIVariables{}
	} else {
		efivars = efibootmgr.RealEFIVariables{}
	}
	if !*noEfivars {
		if bm, err := efibootmgr.NewBootManagerForVariables(efivars); err != nil {
			log.Println("cannot load efi boot variables:", err)
			os.Exit(1)
		} else {
			maybeBm = &bm
		}
	}

	km, err := efibootmgr.NewKernelManager(esp, kernelSourceDir, vendor, maybeBm)
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
	sourceKernels, err := km.GetSourceKernels()
	if err != nil {
		log.Print("Unable to get source kernels: %v", err)
		os.Exit(1)
	}
	targetKernels, err := km.InstallKernels(sourceKernels)
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}

	if err = km.RegisterNewKernelEFIs(); err != nil {
		log.Print(err)
		os.Exit(1)
	}

	// Determine if the fallback mechanism is required
	isCurrentBootLatest := false
	if !(*trustKernelInstalls) {
		if usingRealEFIVars {
			// Only set fallback if the latest kernel is not booted
			isCurrentBootLatest, err = km.IsCurrentBootLatest()
			if err != nil {
				log.Printf("Unable to determine if the latest kernel is BootCurrent: %v", err)
				os.Exit(1)
			}
		}
	}

	if *confirmKernelBoot {
		// If current boot is not latest, a fallback may have occurred
		isCurrentBootLatest, err := km.IsCurrentBootLatest()
		if err != nil {
			log.Printf("Unable to determine if a kernel fallback occurred: %v", err)
			os.Exit(1)
		}
		if !isCurrentBootLatest {
			// Do not ammend the boot loader if a fallback occurred
			log.Printf("BootCurrent is not the latest kernel; fallback may have occurred")
			os.Exit(1)
		}
	}

	if !isCurrentBootLatest {
		if err := km.SetLatestKernelToBootNext(); err != nil {
			log.Printf("Unable to set kernel fallback for new kernel: %v", err)
			os.Exit(1)
		}
		log.Println("Set kernel fallback mechanism for newly installed kernel")
	} else {
		if err = km.CommitToBootLoader(); err != nil {
			log.Print(err)
			os.Exit(1)
		}
		// Cleanup old entries
		if err = km.RemoveObsoleteKernels(); err != nil {
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
