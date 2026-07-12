import SwiftUI

// Real Settings pane, reachable via Cmd-, / the app menu on a regular (dock)
// app — see docs/research/full-app-and-dmg.md §1-2. Four tabs: Defaults
// (NewMachineSheet's @AppStorage knobs), Daemon (install/uninstall/restart
// the LaunchAgent), Advanced (CLI path override), About.
// `model` is inherited from the ancestor `.environmentObject` UmbraApp
// attaches to the Settings scene — no need to re-attach it per tab.
// The top-level TabView registers as an open surface (surfaceAppeared /
// surfaceDisappeared) so the Daemon tab's status reflects live polling
// while Settings is the only open window, same refcount API
// MenuBarView/DashboardView use.

struct SettingsView: View {
    @EnvironmentObject var model: StatusModel

    var body: some View {
        TabView {
            DefaultsSettingsTab()
                .tabItem { Label("Defaults", systemImage: "slider.horizontal.3") }

            DaemonSettingsTab()
                .tabItem { Label("Daemon", systemImage: "bolt.fill") }

            AdvancedSettingsTab()
                .tabItem { Label("Advanced", systemImage: "wrench.and.screwdriver") }

            AboutSettingsTab()
                .tabItem { Label("About", systemImage: "info.circle") }
        }
        .frame(width: 460)
        .frame(minHeight: 320)
        .onAppear { model.surfaceAppeared() }
        .onDisappear { model.surfaceDisappeared() }
    }
}

/// Defaults tab: the same `@AppStorage` keys `NewMachineSheet` reads when
/// seeding its Stepper state, so a change here is picked up next time the
/// "+ New Machine" sheet opens.
private struct DefaultsSettingsTab: View {
    @AppStorage("defaultCPUs") private var defaultCPUs = 4
    @AppStorage("defaultMemoryGiB") private var defaultMemoryGiB = 8
    @AppStorage("defaultDiskGiB") private var defaultDiskGiB = 64

    var body: some View {
        Form {
            Stepper("CPUs: \(defaultCPUs)", value: $defaultCPUs, in: 1...16)
            Stepper("Memory: \(defaultMemoryGiB) GiB", value: $defaultMemoryGiB, in: 1...64)
            Stepper("Disk: \(defaultDiskGiB) GiB", value: $defaultDiskGiB, in: 8...512)
            Text("Used when creating a new machine.")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .padding()
    }
}

/// Daemon tab: shows whether the LaunchAgent is registered and lets the user
/// (re)install/uninstall it, independent of onboarding's bundle-copy flow.
private struct DaemonSettingsTab: View {
    @EnvironmentObject var model: StatusModel

    var body: some View {
        Form {
            LabeledContent("Status") {
                Text(statusText)
                    .foregroundStyle(model.daemonInstalled ? .green : .secondary)
            }

            HStack {
                Button("Install") { Task { await model.daemonInstall() } }
                    .disabled(busy)
                Button("Uninstall") { Task { await model.daemonUninstall() } }
                    .disabled(busy)
                Button("Restart") { Task { await model.restartDaemon() } }
                    .disabled(busy)
                if busy {
                    ProgressView()
                        .scaleEffect(0.6)
                }
            }

            if let installError = model.installError {
                Text(installError)
                    .font(.caption)
                    .foregroundStyle(.red)
            }
        }
        .padding()
    }

    private var busy: Bool { model.busy.contains("daemon") }

    private var statusText: String {
        let daemon = model.status?.daemon ?? "down"
        return model.daemonInstalled ? "installed (daemon: \(daemon))" : "not installed (daemon: \(daemon))"
    }
}

/// Advanced tab: CLI path override (honored first by `umbraCLIPath()`).
private struct AdvancedSettingsTab: View {
    @AppStorage("cliPathOverride") private var cliPathOverride = ""

    var body: some View {
        Form {
            TextField("CLI Path Override", text: $cliPathOverride)
            Text("Leave blank to auto-detect (bundled → Homebrew → /usr/local/bin).")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .padding()
    }
}

/// About tab: app name, bundle version (falls back to "dev" for `swift run`
/// builds with no Info.plist), GitHub link, license.
private struct AboutSettingsTab: View {
    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: "cube.fill")
                .font(.system(size: 40))
                .foregroundStyle(.secondary)
            Text("Umbra")
                .font(.title2)
                .bold()
            Text("Version \(appVersion)")
                .foregroundStyle(.secondary)
            Link("github.com/ForceAI-KW/umbra", destination: URL(string: "https://github.com/ForceAI-KW/umbra")!)
            Text("Apache-2.0 licensed.")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var appVersion: String {
        Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "dev"
    }
}
