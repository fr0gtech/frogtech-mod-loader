package main

import (
	"sync"

	"github.com/fr0gtech/frogtech-mod-loader/internal/icon"
)

// iconResourceID is the exe icon resource written by tools/winres.
const iconResourceID = 1

var iconICO = sync.OnceValue(func() []byte { return icon.ICO(16, 32, 48) })
