// Package discovery advertises the hub on the local network (02j
// "Discovery", ADR 0028, ADR 0030 for the browse side).
//
// [Plan] is the pure part: from the host name, the bound agent listener and
// the machine's interface addresses it builds [Records], the _fleetd._tcp
// service the agent's browser reads. The instance is fleetd-<host>, the SRV
// target fleetd-<host>.local (ca.ServiceTarget, the name the server
// certificate always holds), the port the listener's, and the TXT record only
// proto=<min>-<max>. The addresses are explicit and belong to one interface,
// one per IP family: the agent's library client keeps only the last A record,
// so one interface is chosen (the default route's, else the first real one),
// not all of them, and the responder joins the mDNS group on it. Never the OS
// host name, loopback or IPv6 link-local; IPv4 link-local ranks last; a
// point-to-point interface never; a bridge-named interface only when the
// listener is bound to its address or it holds the default route; a
// tunnel-named one only as the last resort when it holds the route and is not
// point-to-point.
//
// An [Advertiser] decides once, at start, whether to advertise: a refusal
// (discovery.mdns off, no host name, no address, a served certificate that
// lacks the target) or a failure (the interface list, no multicast family
// that binds, the responder) is logged and counted once and never stops the
// hub. [Responder] is the only code that touches the network, the library
// github.com/hashicorp/mdns (ADR 0023); [NewResponder] is the real one and
// tests inject a fake, so the default test run sends no multicast.
package discovery
