# Entitlements & codesigning

- `umbrad` requires `com.apple.security.virtualization` — applied via ad-hoc signing in `make build`. An unsigned/re-linked binary fails at VM creation with a VZErrorDomain error. Always build via make.
- The CLI (`umbra`) creates no VMs → needs no entitlement.
- **Never request `com.apple.vm.networking`** (bridged networking). Apple gates it to vetted vendors (PITFALLS P12, Code-Hex/vz#180). Umbra uses userspace NAT (M1: VZNATNetworkDeviceAttachment; M2: gvisor-tap-vsock) which needs no entitlement. If bridged mode is ever demanded: separately-signed root helper via SMAppService (lima socket_vmnet pattern) — design doc first.
