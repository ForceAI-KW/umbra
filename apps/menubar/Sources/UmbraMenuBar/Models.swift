import Foundation

// Mirrors `internal/client/client.go`'s `MachineView`/`DockerStatus` and
// `cmd/umbra/status.go`'s JSON envelope (`{"daemon":..., "machines":..., "docker":...}`).
// See docs/research/menubar-app.md §3.

enum MachineState: String, Codable {
    case stopped, starting, running, stopping, crashed
}

struct Machine: Codable, Identifiable {
    var id: String { name }
    let name: String
    let state: MachineState
    let ip: String?
    let sshPort: Int?
    let cpus: Int
    let memoryMiB: Int
    let diskGiB: Int
    let image: String?
    let autostart: Bool
    let zombie: Bool?

    enum CodingKeys: String, CodingKey {
        case name, state, ip, cpus, image, autostart, zombie
        case sshPort = "ssh_port"
        case memoryMiB = "memory_mib"
        case diskGiB = "disk_gib"
    }
}

struct DockerStatus: Codable {
    let installed: Bool
    let running: Bool
    let ip: String?
    let socket: String?
    let contextCurrent: Bool

    enum CodingKeys: String, CodingKey {
        case installed, running, ip, socket
        case contextCurrent = "context_current"
    }
}

struct StatusResponse: Codable {
    let daemon: String              // "up" | "down"
    let error: String?              // present when daemon == "down"
    let machines: [Machine]?
    let docker: DockerStatus?
}
