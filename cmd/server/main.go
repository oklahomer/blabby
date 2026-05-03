package main

func main() {
	// TODO: Initialize and start the blabby chat server.
	//
	// When wiring the cluster, refuse to start in cluster mode with an empty
	// remote.Config.AdvertisedHost: the fallback (lis.Addr().String()) is
	// unsafe under 0.0.0.0 binds, containers, or NAT — peer nodes silently
	// dead-letter every fan-out to a registered PID. A dev/single-node
	// escape hatch is acceptable, but the default must not auto-derive
	// from the listener. Log the resolved advertised address at startup.
	// See docs/adr/adr-011-cross-boundary-pid-propagation.md.
}
