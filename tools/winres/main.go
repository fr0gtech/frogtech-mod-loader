// Command winres writes the Windows resources (icon, manifest, version info)
// for FrogLoader.exe into a .syso object that `go build` links in.
//
//	go run ./tools/winres -version 0.2.0 -o rsrc_windows_amd64.syso
package main

import (
	"flag"
	"fmt"
	"image"
	"log"
	"os"
	"strings"

	"github.com/fr0gtech/frogtech-mod-loader/internal/icon"
	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
)

func main() {
	ver := flag.String("version", "0.0.0", "product version (x.y.z)")
	out := flag.String("o", "rsrc_windows_amd64.syso", "output .syso")
	flag.Parse()

	rs := &winres.ResourceSet{}

	var imgs []image.Image
	for _, s := range icon.Sizes {
		imgs = append(imgs, icon.Render(s))
	}
	ico, err := winres.NewIconFromImages(imgs)
	if err != nil {
		log.Fatal(err)
	}
	if err := rs.SetIcon(winres.ID(1), ico); err != nil {
		log.Fatal(err)
	}

	rs.SetManifest(winres.AppManifest{
		Identity:            winres.AssemblyIdentity{Name: "Frogtech.FrogLoader", Version: quad(*ver)},
		Description:         "Frogtech Mod Loader",
		Compatibility:       winres.Win10AndAbove,
		ExecutionLevel:      winres.AsInvoker,
		DPIAwareness:        winres.DPIPerMonitorV2,
		UseCommonControlsV6: true,
		LongPathAware:       true,
	})

	vi := version.Info{}
	vi.SetFileVersion(*ver)
	vi.SetProductVersion(*ver)
	const en = 0x0409
	for k, v := range map[string]string{
		version.CompanyName:      "Frogtech",
		version.ProductName:      "Frogtech Mod Loader",
		version.FileDescription:  "Frogtech Mod Loader - installs and updates Deadlock mods",
		version.FileVersion:      *ver,
		version.ProductVersion:   *ver,
		version.InternalName:     "FrogLoader",
		version.OriginalFilename: "FrogLoader.exe",
		version.LegalCopyright:   "Copyright (c) 2026 Frogtech", // fixed: the build must be reproducible
		version.Comments:         "https://mods.frogtech.dev",
	} {
		if err := vi.Set(en, k, v); err != nil {
			log.Fatal(err)
		}
	}
	rs.SetVersionInfo(vi)

	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	if err := rs.WriteObject(f, winres.ArchAMD64); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
}

// quad turns "0.2.0" or "0.2.0-dev" into a manifest version "0.2.0.0".
func quad(v string) [4]uint16 {
	v, _, _ = strings.Cut(v, "-")
	var q [4]uint16
	for i, p := range strings.SplitN(v, ".", 4) {
		var n int
		fmt.Sscanf(p, "%d", &n)
		q[i] = uint16(n)
	}
	return q
}
