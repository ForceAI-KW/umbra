import XCTest
@testable import UmbraMenuBar

final class ModelsTests: XCTestCase {
    func testDecodesDaemonUpWithRunningMachine() throws {
        let json = """
        {
          "daemon": "up",
          "machines": [
            {
              "name": "dev",
              "state": "running",
              "ip": "192.168.64.2",
              "ssh_port": 51234,
              "cpus": 2,
              "memory_mib": 2048,
              "disk_gib": 20,
              "image": "ubuntu-24.04",
              "autostart": true
            }
          ],
          "docker": {
            "installed": false,
            "running": false,
            "context_current": false
          }
        }
        """
        let data = Data(json.utf8)
        let status = try JSONDecoder().decode(StatusResponse.self, from: data)

        XCTAssertEqual(status.daemon, "up")
        XCTAssertNil(status.error)
        XCTAssertEqual(status.machines?.count, 1)

        let machine = try XCTUnwrap(status.machines?.first)
        XCTAssertEqual(machine.name, "dev")
        XCTAssertEqual(machine.state, .running)
        XCTAssertEqual(machine.sshPort, 51234)
        XCTAssertEqual(machine.memoryMiB, 2048)
        XCTAssertEqual(machine.diskGiB, 20)
        XCTAssertEqual(machine.cpus, 2)
        XCTAssertEqual(machine.ip, "192.168.64.2")
        XCTAssertEqual(machine.image, "ubuntu-24.04")
        XCTAssertTrue(machine.autostart)
        XCTAssertNil(machine.zombie)

        let docker = try XCTUnwrap(status.docker)
        XCTAssertFalse(docker.installed)
        XCTAssertFalse(docker.running)
        XCTAssertFalse(docker.contextCurrent)
    }

    func testDecodesDaemonDown() throws {
        let json = """
        {"daemon":"down","error":"dial unix /Users/x/.umbra/run/api.sock: connect: connection refused"}
        """
        let data = Data(json.utf8)
        let status = try JSONDecoder().decode(StatusResponse.self, from: data)

        XCTAssertEqual(status.daemon, "down")
        XCTAssertNotNil(status.error)
        XCTAssertNil(status.machines)
        XCTAssertNil(status.docker)
    }

    func testDecodesCrashedZombieMachine() throws {
        let json = """
        {
          "name": "broken",
          "state": "crashed",
          "cpus": 1,
          "memory_mib": 1024,
          "disk_gib": 10,
          "autostart": false,
          "zombie": true
        }
        """
        let data = Data(json.utf8)
        let machine = try JSONDecoder().decode(Machine.self, from: data)

        XCTAssertEqual(machine.state, .crashed)
        XCTAssertEqual(machine.zombie, true)
        XCTAssertNil(machine.sshPort)
        XCTAssertNil(machine.ip)
        XCTAssertNil(machine.image)
        XCTAssertFalse(machine.autostart)
        XCTAssertEqual(machine.id, "broken")
    }
}
