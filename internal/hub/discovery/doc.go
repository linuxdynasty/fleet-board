// Package discovery advertises the hub on the local network (02j
// "Discovery", ADR 0028, ADR 0030 for the browse side).
//
// [Plan] is the pure part: from the host name, the bound agent listener and
// the machine's interface addresses it builds [Records], the _fleetd._tcp
// service the agent's browser reads. The instance is fleetd-<host>, the SRV
// target fleetd-<host>.local (ca.ServiceTarget, the name the server
// certificate always holds), the port the listener's, the addresses explicit
// unicast non-loopback ones (never the OS host name, never IPv6 link-local),
// and the TXT record only proto=<min>-<max>.
//
// An [Advertiser] decides once, at start, whether to advertise: a refusal
// (discovery.mdns off, no host name, no address, a served certificate that
// lacks the target) or a failure (the interface list, no multicast family
// that binds, the responder) is logged and counted once and never stops the
// hub. [Responder] is the only code that touches the network, the library
// github.com/hashicorp/mdns (ADR 0023); [NewResponder] is the real one and
// tests inject a fake, so the default test run sends no multicast.
package discovery
