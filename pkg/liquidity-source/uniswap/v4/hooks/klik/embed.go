package klik

import _ "embed"

//go:embed abis/Hook.json
var hookABIJson []byte

//go:embed abis/Factory.json
var factoryABIJson []byte
