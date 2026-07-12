import Foundation

// View model driving the MenuBarExtra window: resolves the CLI path once,
// polls `umbra status --json` on a 2s cadence only while the window is
// open (docs/research/menubar-app.md §4), and issues start/stop/docker
// actions via CLI, re-fetching status immediately after (§5).

@MainActor
final class StatusModel: ObservableObject {
    @Published var status: StatusResponse?
    @Published var cliMissing: Bool = false
    @Published var busy: Set<String> = []

    private let cli: CLI?
    private var pollTask: Task<Void, Never>?

    init() {
        if let path = umbraCLIPath() {
            cli = CLI(path: path)
        } else {
            cli = nil
            cliMissing = true
        }
    }

    /// Starts the 2s poll loop. Idempotent — calling while already polling
    /// is a no-op (guards against double `.onAppear` firing).
    func startPolling() {
        guard pollTask == nil else { return }
        pollTask = Task { [weak self] in
            while let self, !Task.isCancelled {
                await self.refresh()
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
        }
    }

    /// Cancels the poll loop. Safe to call even if not currently polling.
    func stopPolling() {
        pollTask?.cancel()
        pollTask = nil
    }

    /// Fetches status from the CLI. On failure (daemon down, CLI missing,
    /// decode error) sets a synthetic "down" status rather than crashing or
    /// leaving stale state — the popover should always render something.
    func refresh() async {
        guard let cli else {
            status = StatusResponse(daemon: "down", error: "umbra CLI not found", machines: nil, docker: nil)
            return
        }
        do {
            status = try await cli.status()
        } catch {
            status = StatusResponse(daemon: "down", error: "\(error)", machines: nil, docker: nil)
        }
    }

    /// Starts a stopped/crashed machine, or stops a running one. Marks the
    /// machine busy for the duration (the CLI call blocks until the daemon
    /// confirms the new state — §5), then refreshes.
    func toggleMachine(_ machine: Machine) async {
        guard let cli else { return }
        busy.insert(machine.name)
        defer { busy.remove(machine.name) }
        do {
            if machine.state == .running {
                try await cli.stop(machine.name)
            } else {
                try await cli.start(machine.name)
            }
        } catch {
            // Best-effort: the refresh below will surface the resulting
            // state (or lack of change) regardless of success/failure.
        }
        await refresh()
    }

    /// Starts/stops the Docker VM, mirroring `toggleMachine`'s busy/refresh
    /// pattern under a fixed "docker" busy key.
    func toggleDocker() async {
        guard let cli else { return }
        busy.insert("docker")
        defer { busy.remove("docker") }
        do {
            if status?.docker?.running == true {
                try await cli.dockerStop()
            } else {
                try await cli.dockerStart()
            }
        } catch {
            // Best-effort, same rationale as toggleMachine.
        }
        await refresh()
    }

    /// Hands off to Terminal.app via the CLI's `umbra shell` (§5) — no
    /// status refresh needed, this doesn't change daemon state.
    func openShell(_ machine: Machine) {
        cli?.openShell(machineName: machine.name)
    }
}
