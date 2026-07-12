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
    @Published var rosettaStatus: String = "unknown"
    @Published var onboardingNeeded: Bool = false

    /// Standard install location LaunchAgent's `--bin` points at (§3,
    /// docs/research/full-app-and-dmg.md — Option A), also what
    /// `installToUsrLocal` copies onto.
    private static let installedUmbradPath = "/usr/local/bin/umbrad"

    private let cli: CLI?
    private var pollTask: Task<Void, Never>?
    private var isRefreshing = false

    /// Refcount of open surfaces (menu bar popover, dashboard window) that
    /// want live polling. Polling runs while count > 0, on one shared task.
    private var openSurfaces = 0

    init() {
        if let path = umbraCLIPath() {
            cli = CLI(path: path)
        } else {
            cli = nil
            cliMissing = true
        }
        onboardingNeeded = cliMissing
    }

    /// A surface (view) appeared — starts the shared poll loop if it wasn't
    /// already running. Call from `.onAppear`.
    func surfaceAppeared() {
        openSurfaces += 1
        guard openSurfaces == 1 else { return }
        startPolling()
    }

    /// A surface (view) disappeared — stops the shared poll loop once no
    /// surface needs it. Call from `.onDisappear`.
    func surfaceDisappeared() {
        openSurfaces = max(0, openSurfaces - 1)
        guard openSurfaces == 0 else { return }
        stopPolling()
    }

    /// Starts the 2s poll loop. Idempotent — calling while already polling
    /// is a no-op.
    private func startPolling() {
        guard pollTask == nil else { return }
        pollTask = Task { [weak self] in
            while let self, !Task.isCancelled {
                await self.refresh()
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
        }
    }

    /// Cancels the poll loop. Safe to call even if not currently polling.
    private func stopPolling() {
        pollTask?.cancel()
        pollTask = nil
    }

    /// Fetches status from the CLI. On failure (daemon down, CLI missing,
    /// decode error) sets a synthetic "down" status rather than crashing or
    /// leaving stale state — the popover should always render something.
    /// Coalesced: a refresh already in flight (e.g. two surfaces open at
    /// once, or a manual action's own refresh overlapping the poll tick)
    /// makes this a no-op rather than stacking a second request.
    func refresh() async {
        guard !isRefreshing else { return }
        isRefreshing = true
        defer { isRefreshing = false }

        guard let cli else {
            status = StatusResponse(daemon: "down", error: "umbra CLI not found", machines: nil, docker: nil)
            onboardingNeeded = true
            return
        }
        do {
            status = try await cli.status()
        } catch {
            status = StatusResponse(daemon: "down", error: "\(error)", machines: nil, docker: nil)
        }
        if let rosettaResult = try? await cli.rosetta() {
            rosettaStatus = rosettaResult
        }
        onboardingNeeded = cliMissing || status?.daemon == "down"
    }

    /// Creates a machine (then starts it, per `CLI.create`), marking it busy
    /// for the duration, mirroring `toggleMachine`'s busy/refresh pattern.
    func createMachine(_ name: String, cpus: Int, memoryGiB: Int, diskGiB: Int) async {
        guard let cli else { return }
        busy.insert(name)
        defer { busy.remove(name) }
        do {
            try await cli.create(name, cpus: cpus, memoryGiB: memoryGiB, diskGiB: diskGiB)
        } catch {
            // Best-effort, same rationale as toggleMachine.
        }
        await refresh()
    }

    /// Deletes a machine, mirroring `toggleMachine`'s busy/refresh pattern.
    func deleteMachine(_ name: String) async {
        guard let cli else { return }
        busy.insert(name)
        defer { busy.remove(name) }
        do {
            try await cli.remove(name)
        } catch {
            // Best-effort, same rationale as toggleMachine.
        }
        await refresh()
    }

    /// Re-registers the LaunchAgent against the standard install path —
    /// a Settings-pane "reinstall daemon" action for after a rebuild
    /// replaces `/usr/local/bin/umbrad` (docs/research/full-app-and-dmg.md §6).
    func daemonInstall() async {
        guard let cli else { return }
        do {
            try await cli.daemonInstall(binPath: Self.installedUmbradPath)
        } catch {
            // Best-effort, same rationale as toggleMachine.
        }
        await refresh()
    }

    func daemonUninstall() async {
        guard let cli else { return }
        do {
            try await cli.daemonUninstall()
        } catch {
            // Best-effort, same rationale as toggleMachine.
        }
        await refresh()
    }

    /// First-run onboarding install (§3, docs/research/full-app-and-dmg.md):
    /// resolves the bundled `umbra`/`umbrad`/entitlements and copies them to
    /// `/usr/local/bin` via `CLI.installToUsrLocal`, then registers the
    /// LaunchAgent. No-op if any bundled resource is missing (dev builds run
    /// via `swift run` have no app bundle to resolve these from).
    func installDaemon() async {
        guard let cli else { return }
        guard let bundledUmbra = Bundle.main.url(forAuxiliaryExecutable: "umbra")?.path,
              let bundledUmbrad = Bundle.main.url(forAuxiliaryExecutable: "umbrad")?.path,
              let entitlements = Bundle.main.url(forResource: "vz", withExtension: "entitlements")?.path
        else { return }
        do {
            try await cli.installToUsrLocal(bundledUmbra: bundledUmbra, bundledUmbrad: bundledUmbrad, entitlements: entitlements)
        } catch {
            // Best-effort, same rationale as toggleMachine.
        }
        await refresh()
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
