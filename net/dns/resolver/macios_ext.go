// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

//go:build ts_macext && (darwin || ios)

package resolver

import (
	"errors"
	"net"

	"tailscale.com/net/netns"
	"tailscale.com/wgengine/monitor"
)

func init() {
	initListenConfig = initListenConfigNetworkExtension
}

func initListenConfigNetworkExtension(nc *net.ListenConfig, mon *monitor.Mon, tunName string) error {
	nif, ok := mon.InterfaceState().Interface[tunName]
	if !ok {
		return errors.New("utun not found")
	}
	return netns.SetListenConfigInterfaceIndex(nc, nif.Interface.Index)
}
