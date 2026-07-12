import Foundation

// Thin client over the `umbra` CLI binary — shells out instead of hand-rolling
// HTTP-over-unix-socket, so the retry/backoff (P10) and JSON envelope logic
// live once, in Go (`internal/client/client.go`). See docs/research/menubar-app.md §2.

enum CLIError: Error {
    case notFound
    case nonZeroExit(Int32, String)
    case spawnFailed(Error)
}

/// Resolution order for the `umbra` CLI binary, first existing path wins.
/// See docs/research/menubar-app.md §2b. `Process` does not inherit the
/// invoking user's shell `PATH`, so every candidate must be an absolute path.
func umbraCLIPath() -> String? {
    var candidates: [String] = []

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

/// Runs the `umbra` CLI with `args`, returning stdout on exit 0.
/// Process + two Pipes + terminationHandler, per docs/research/menubar-app.md §2 —
/// not `waitUntilExit()` on the main actor, which can deadlock on a full pipe buffer.
func runUmbra(_ args: [String], cliPath: String) async throws -> Data {
    try await withCheckedThrowingContinuation { continuation in
        let process = Process()
        process.executableURL = URL(fileURLWithPath: cliPath)
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

/// Escapes a string for embedding inside an AppleScript double-quoted string
/// literal. Order matters: backslashes must be escaped before quotes, else
/// the backslashes inserted for quotes would themselves get re-escaped.
func appleScriptEscape(_ s: String) -> String {
    s.replacingOccurrences(of: "\\", with: "\\\\")
        .replacingOccurrences(of: "\"", with: "\\\"")
}

/// AppleScript that opens Terminal.app running `umbra shell <machineName>`.
/// Delegates to the CLI's own `umbra shell` (`cmd/umbra/shell.go`) rather than
/// reconstructing the `ssh` invocation here, so the exact args live once.
/// See docs/research/menubar-app.md §5.
func openShellScript(machineName: String) -> String {
    let sshCmd = "umbra shell \(machineName)"
    let escaped = appleScriptEscape(sshCmd)
    return "tell application \"Terminal\" to do script \"\(escaped)\""
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

    /// Best-effort: hands off to Terminal.app via in-process AppleScript
    /// (`NSAppleScript`, not an `osascript` subprocess). Logs and swallows
    /// errors rather than throwing — a failed shell handoff shouldn't crash
    /// the popover's action flow.
    func openShell(machineName: String) {
        let script = openShellScript(machineName: machineName)
        guard let appleScript = NSAppleScript(source: script) else {
            FileHandle.standardError.write(Data("openShell: failed to construct NSAppleScript\n".utf8))
            return
        }
        var errorInfo: NSDictionary?
        appleScript.executeAndReturnError(&errorInfo)
        if let errorInfo {
            FileHandle.standardError.write(Data("openShell: AppleScript error: \(errorInfo)\n".utf8))
        }
    }
}
