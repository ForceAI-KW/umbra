import Foundation

// Thin client over the `umbra` CLI binary — shells out instead of hand-rolling
// HTTP-over-unix-socket, so the retry/backoff (P10) and JSON envelope logic
// live once, in Go (`internal/client/client.go`). See docs/research/menubar-app.md §2.

enum CLIError: Error {
    case nonZeroExit(Int32, String)
    case spawnFailed(Error)
}

extension CLIError: LocalizedError {
    /// Human-readable message for surfacing in the UI (Settings/Onboarding),
    /// rather than the generic "operation couldn't be completed" `Error`
    /// default `localizedDescription` would otherwise produce.
    var errorDescription: String? {
        switch self {
        case .nonZeroExit(let status, let message):
            return message.isEmpty ? "Command exited with status \(status)." : message
        case .spawnFailed(let underlying):
            return "Failed to launch process: \(underlying.localizedDescription)"
        }
    }
}

/// Resolution order for the `umbra` CLI binary, first existing path wins.
/// See docs/research/menubar-app.md §2b. `Process` does not inherit the
/// invoking user's shell `PATH`, so every candidate must be an absolute path.
/// The Settings "Advanced" tab's CLI path override (stored under the
/// `"cliPathOverride"` UserDefaults key via `@AppStorage`) is checked FIRST,
/// ahead of the bundled binary, so it always wins when set.
func umbraCLIPath() -> String? {
    var candidates: [String] = []

    if let override = UserDefaults.standard.string(forKey: "cliPathOverride"), !override.isEmpty {
        candidates.append(override)
    }
    if let bundled = Bundle.main.url(forAuxiliaryExecutable: "umbra")?.path {
        candidates.append(bundled)
    }
    candidates.append("/opt/homebrew/bin/umbra")
    candidates.append("/usr/local/bin/umbra")
    if let envPath = ProcessInfo.processInfo.environment["UMBRA_CLI_PATH"] {
        candidates.append(envPath)
    }
    candidates.append(NSString(string: "~/Desktop/projects/umbra/bin/umbra").expandingTildeInPath)

    return resolveCLIPath(candidates: candidates)
}

/// Pure helper: returns the first candidate that exists on disk, or nil.
/// Split out from `umbraCLIPath()` so tests can inject temp paths instead
/// of depending on the real filesystem/bundle/env.
func resolveCLIPath(candidates: [String]) -> String? {
    for candidate in candidates where FileManager.default.fileExists(atPath: candidate) {
        return candidate
    }
    return nil
}

/// Runs an arbitrary executable at `path` with `args`, returning stdout on
/// exit 0. Process + two Pipes + terminationHandler, per
/// docs/research/menubar-app.md §2 — not `waitUntilExit()` on the main actor,
/// which can deadlock on a full pipe buffer. Shared by `runUmbra` and the
/// `cp`/`codesign`/`osascript` calls `installToUsrLocal` makes (those aren't
/// `umbra` itself, so they go through this directly).
func runProcess(_ path: String, _ args: [String]) async throws -> Data {
    try await withCheckedThrowingContinuation { continuation in
        let process = Process()
        process.executableURL = URL(fileURLWithPath: path)
        process.arguments = args
        process.environment = ProcessInfo.processInfo.environment

        let stdout = Pipe()
        let stderr = Pipe()
        process.standardOutput = stdout
        process.standardError = stderr

        process.terminationHandler = { proc in
            let outData = stdout.fileHandleForReading.readDataToEndOfFile()
            let errData = stderr.fileHandleForReading.readDataToEndOfFile()
            if proc.terminationStatus == 0 {
                continuation.resume(returning: outData)
            } else {
                let msg = String(data: errData, encoding: .utf8) ?? ""
                continuation.resume(throwing: CLIError.nonZeroExit(proc.terminationStatus, msg))
            }
        }

        do {
            try process.run()
        } catch {
            continuation.resume(throwing: CLIError.spawnFailed(error))
        }
    }
}

/// Runs the `umbra` CLI with `args`, returning stdout on exit 0.
func runUmbra(_ args: [String], cliPath: String) async throws -> Data {
    try await runProcess(cliPath, args)
}

/// Pure helper: builds the arg array for `umbra create`. Split out so tests
/// can assert the exact CLI invocation without shelling out. A non-empty
/// `role` (e.g. "ci-runner") appends `--role <role>`; nil/empty creates a
/// plain machine (the CLI's default role).
func createArgs(name: String, cpus: Int, memoryGiB: Int, diskGiB: Int, role: String? = nil) -> [String] {
    var args = ["create", name, "--cpus", String(cpus), "--memory-gib", String(memoryGiB), "--disk-gib", String(diskGiB)]
    if let role, !role.isEmpty {
        args += ["--role", role]
    }
    return args
}

/// Pure helper: parses `umbra rosetta status`'s human-readable stdout
/// (`cmd/umbra/rosetta.go`: "Rosetta: installed|not installed|not supported")
/// into a terse token the app can switch on. Unrecognized output → "unknown".
func parseRosettaStatus(_ output: String) -> String {
    if output.contains("Rosetta: installed") { return "installed" }
    if output.contains("Rosetta: not installed") { return "notInstalled" }
    if output.contains("Rosetta: not supported") { return "notSupported" }
    return "unknown"
}

/// Wraps `s` in single quotes for safe embedding in a shell command line,
/// escaping any embedded single quotes (`'` → `'\''`).
func shellQuote(_ s: String) -> String {
    "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'"
}

/// Pure helper: the shell command (mkdir + cp + re-sign) run with elevated
/// privileges when `/usr/local/bin` isn't writable. Mirrors
/// `scripts/install.sh`'s `$SUDO cp` + re-sign-with-entitlements steps.
func adminInstallShellCommand(umbra: String, umbrad: String, entitlements: String) -> String {
    [
        "mkdir -p /usr/local/bin",
        "cp \(shellQuote(umbrad)) /usr/local/bin/umbrad",
        "cp \(shellQuote(umbra)) /usr/local/bin/umbra",
        "codesign --force --entitlements \(shellQuote(entitlements)) --sign - /usr/local/bin/umbrad",
        "codesign --force --sign - /usr/local/bin/umbra",
    ].joined(separator: " && ")
}

/// AppleScript that requests one administrator-privileges elevation to run
/// `adminInstallShellCommand`'s copy+re-sign, same escaping pattern as
/// `openShellScript`.
func adminInstallScript(umbra: String, umbrad: String, entitlements: String) -> String {
    let shellCmd = adminInstallShellCommand(umbra: umbra, umbrad: umbrad, entitlements: entitlements)
    let escaped = appleScriptEscape(shellCmd)
    return "do shell script \"\(escaped)\" with administrator privileges"
}

/// Escapes a string for embedding inside an AppleScript double-quoted string
/// literal. Order matters: backslashes must be escaped before quotes, else
/// the backslashes inserted for quotes would themselves get re-escaped.
func appleScriptEscape(_ s: String) -> String {
    s.replacingOccurrences(of: "\\", with: "\\\\")
        .replacingOccurrences(of: "\"", with: "\\\"")
}

/// Body of a `.command` launcher that runs `umbra shell <machineName>` in
/// Terminal. Opening a `.command` file starts Terminal via a normal file-open
/// — it needs NONE of the Apple Events ("control Terminal") permission that a
/// `tell application "Terminal"` script requires. An ad-hoc-signed app can't
/// reliably obtain that Automation grant, which is why the old AppleScript
/// Shell handoff silently did nothing. `umbraPath` is the resolved ABSOLUTE
/// CLI path (a fresh Terminal doesn't inherit the app's PATH); both it and the
/// machine name are single-quoted for the shell. Delegates to the CLI's own
/// `umbra shell` (`cmd/umbra/shell.go`) so the ssh args live in one place.
func shellCommandFileBody(umbraPath: String, machineName: String) -> String {
    "#!/bin/bash\nexec \(shellQuote(umbraPath)) shell \(shellQuote(machineName))\n"
}

/// Typed wrappers over `runUmbra`, used by the app's view model. No business
/// logic beyond spawning the CLI and decoding its output.
struct CLI {
    let path: String

    func status() async throws -> StatusResponse {
        let data = try await runUmbra(["status", "--json"], cliPath: path)
        return try JSONDecoder().decode(StatusResponse.self, from: data)
    }

    func start(_ name: String) async throws {
        _ = try await runUmbra(["start", name], cliPath: path)
    }

    func stop(_ name: String) async throws {
        _ = try await runUmbra(["stop", name], cliPath: path)
    }

    func dockerStart() async throws {
        _ = try await runUmbra(["docker", "start"], cliPath: path)
    }

    func dockerStop() async throws {
        _ = try await runUmbra(["docker", "stop"], cliPath: path)
    }

    /// Provisions the reserved docker VM (get.docker.com via cloud-init) and
    /// boots it — the `umbra docker install` + `docker start` the CLI does in
    /// two steps. Long-running (first run downloads the Ubuntu image).
    func dockerInstall() async throws {
        _ = try await runUmbra(["docker", "install"], cliPath: path)
        _ = try await runUmbra(["docker", "start"], cliPath: path)
    }

    /// Creates a machine, then starts it — `umbra create` alone only
    /// registers it (`cmd/umbra/create.go`), the daemon leaves it stopped.
    func create(_ name: String, cpus: Int, memoryGiB: Int, diskGiB: Int, role: String? = nil) async throws {
        _ = try await runUmbra(createArgs(name: name, cpus: cpus, memoryGiB: memoryGiB, diskGiB: diskGiB, role: role), cliPath: path)
        _ = try await runUmbra(["start", name], cliPath: path)
    }

    func remove(_ name: String) async throws {
        _ = try await runUmbra(["rm", name], cliPath: path)
    }

    func daemonInstall(binPath: String) async throws {
        _ = try await runUmbra(["daemon", "install", "--bin", binPath], cliPath: path)
    }

    func daemonUninstall() async throws {
        _ = try await runUmbra(["daemon", "uninstall"], cliPath: path)
    }

    /// Reports host Rosetta-for-Linux availability, parsed from
    /// `umbra rosetta status`'s human-readable stdout (`cmd/umbra/rosetta.go`).
    func rosetta() async throws -> String {
        let data = try await runUmbra(["rosetta", "status"], cliPath: path)
        let output = String(data: data, encoding: .utf8) ?? ""
        return parseRosettaStatus(output)
    }

    /// Onboarding install, mirroring `scripts/install.sh`: copies the
    /// bundled `umbra`+`umbrad` into `/usr/local/bin`, re-signs `umbrad`
    /// with its virtualization entitlement (a raw `cp` doesn't reliably
    /// preserve a Mach-O signature — install.sh re-signs explicitly for the
    /// same reason), re-signs `umbra` plain, then registers the LaunchAgent
    /// via `daemon install`. If `/usr/local/bin` isn't writable, does the
    /// whole copy+sign as one `osascript` administrator-privileges elevation
    /// (one prompt) instead of per-command sudo, same as install.sh's
    /// writable-dir check collapsed to a single prompt.
    func installToUsrLocal(bundledUmbra: String, bundledUmbrad: String, entitlements: String) async throws {
        let binDir = "/usr/local/bin"
        let fm = FileManager.default

        let binDirWritable: Bool
        if fm.fileExists(atPath: binDir) {
            binDirWritable = fm.isWritableFile(atPath: binDir)
        } else {
            binDirWritable = fm.isWritableFile(atPath: (binDir as NSString).deletingLastPathComponent)
        }

        if binDirWritable {
            if !fm.fileExists(atPath: binDir) {
                try fm.createDirectory(atPath: binDir, withIntermediateDirectories: true)
            }
            _ = try await runProcess("/bin/cp", [bundledUmbrad, "\(binDir)/umbrad"])
            _ = try await runProcess("/bin/cp", [bundledUmbra, "\(binDir)/umbra"])
            _ = try await runProcess("/usr/bin/codesign", ["--force", "--entitlements", entitlements, "--sign", "-", "\(binDir)/umbrad"])
            _ = try await runProcess("/usr/bin/codesign", ["--force", "--sign", "-", "\(binDir)/umbra"])
        } else {
            let script = adminInstallScript(umbra: bundledUmbra, umbrad: bundledUmbrad, entitlements: entitlements)
            _ = try await runProcess("/usr/bin/osascript", ["-e", script])
        }

        try await daemonInstall(binPath: "\(binDir)/umbrad")
    }

    /// Best-effort: opens `umbra shell <machineName>` in Terminal by writing a
    /// `.command` launcher to ~/.umbra/run and handing it to `open`. This
    /// deliberately AVOIDS `tell application "Terminal"` (Apple Events), whose
    /// Automation permission an ad-hoc-signed app can't reliably get — that gap
    /// made the Shell button silently do nothing. Runs off the main actor and
    /// logs/swallows errors rather than throwing.
    nonisolated func openShell(machineName: String) {
        let runDir = ("~/.umbra/run" as NSString).expandingTildeInPath
        // Filesystem-safe file name; the real (create-time-validated) machine
        // name still goes into the script body, single-quoted.
        let safeName = machineName.replacingOccurrences(of: "/", with: "_")
        let scriptPath = (runDir as NSString).appendingPathComponent("shell-\(safeName).command")
        let body = shellCommandFileBody(umbraPath: path, machineName: machineName)
        Task.detached {
            do {
                try FileManager.default.createDirectory(atPath: runDir, withIntermediateDirectories: true)
                try body.write(toFile: scriptPath, atomically: true, encoding: .utf8)
                try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: scriptPath)
                let proc = Process()
                proc.executableURL = URL(fileURLWithPath: "/usr/bin/open")
                proc.arguments = [scriptPath]
                try proc.run()
            } catch {
                FileHandle.standardError.write(Data("openShell: \(error)\n".utf8))
            }
        }
    }
}
