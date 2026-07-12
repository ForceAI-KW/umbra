import SwiftUI
import AppKit

// Main dashboard window: a NavigationSplitView (machine list sidebar +
// machine detail) once the daemon is reachable, or a placeholder while
// first-run onboarding is needed. Shares `StatusModel` with the
// MenuBarExtra popover via `.environmentObject` (docs/research/
// full-app-and-dmg.md §1). Reuses MenuBarView's dot-color / state-label /
// docker-text conventions: running=green, starting/stopping=yellow,
// stopped=gray, crashed/zombie=red; "crashed*" label when zombie==true.

struct DashboardView: View {
    @EnvironmentObject var model: StatusModel
    @State private var selected: String?
    @State private var showNewMachine = false

    var body: some View {
        Group {
            if model.onboardingNeeded {
                OnboardingPlaceholder()
            } else {
                splitView
            }
        }
        .frame(minWidth: 620, minHeight: 420)
        .onAppear { model.surfaceAppeared() }
        .onDisappear { model.surfaceDisappeared() }
        .sheet(isPresented: $showNewMachine) {
            NewMachineSheet()
                .environmentObject(model)
        }
    }

    private var splitView: some View {
        NavigationSplitView {
            sidebar
                .toolbar {
                    ToolbarItem {
                        Button {
                            showNewMachine = true
                        } label: {
                            Image(systemName: "plus")
                        }
                        .help("New Machine")
                    }
                }
        } detail: {
            if let machine = selectedMachine {
                MachineDetailView(machine: machine)
            } else {
                detailPlaceholder
            }
        }
    }

    private var selectedMachine: Machine? {
        model.status?.machines?.first { $0.name == selected }
    }

    private var sidebar: some View {
        VStack(alignment: .leading, spacing: 0) {
            header

            List(selection: $selected) {
                ForEach(model.status?.machines ?? []) { machine in
                    machineRow(machine).tag(machine.name)
                }
            }
            .listStyle(.sidebar)

            Spacer(minLength: 0)
            Divider()
            footer
        }
    }

    private var header: some View {
        HStack {
            statusDot(color: headerDotColor)
            Text("Umbra").bold()
            Spacer()
            settingsButton
        }
        .padding()
    }

    // `SettingsLink` is macOS 14+ only; the package's deployment target is
    // macOS 13 (README/Makefile documented requirement, matches
    // Virtualization.framework's floor) so fall back to the same
    // `showSettingsWindow:` action SwiftUI itself wires the app menu's
    // "Preferences…" item to, on 13.
    @ViewBuilder
    private var settingsButton: some View {
        if #available(macOS 14.0, *) {
            SettingsLink {
                Image(systemName: "gearshape")
            }
            .buttonStyle(.borderless)
        } else {
            Button {
                NSApp.sendAction(Selector(("showSettingsWindow:")), to: nil, from: nil)
            } label: {
                Image(systemName: "gearshape")
            }
            .buttonStyle(.borderless)
        }
    }

    private var headerDotColor: Color {
        if model.cliMissing { return .gray }
        switch model.status?.daemon {
        case "up": return .green
        case "down": return .red
        default: return .gray
        }
    }

    private func machineRow(_ machine: Machine) -> some View {
        HStack {
            statusDot(color: machineDotColor(machine))
            Text(machine.name)
            Text(stateLabel(machine))
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }

    private func machineDotColor(_ machine: Machine) -> Color {
        if machine.zombie == true { return .red }
        switch machine.state {
        case .running: return .green
        case .starting, .stopping: return .yellow
        case .stopped: return .gray
        case .crashed: return .red
        }
    }

    private func stateLabel(_ machine: Machine) -> String {
        machine.zombie == true ? "crashed*" : machine.state.rawValue
    }

    private var footer: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text("Docker")
                Text(dockerStatusText)
                    .foregroundStyle(.secondary)
                Spacer()
                if model.status?.docker?.installed == true {
                    Button {
                        Task { await model.toggleDocker() }
                    } label: {
                        if model.busy.contains("docker") {
                            ProgressView().scaleEffect(0.5)
                        } else {
                            Image(systemName: model.status?.docker?.running == true ? "stop.fill" : "play.fill")
                        }
                    }
                    .buttonStyle(.borderless)
                }
            }
            Text("Rosetta: \(rosettaStatusText)")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .padding()
    }

    private var dockerStatusText: String {
        guard let docker = model.status?.docker else { return "not installed" }
        if !docker.installed { return "not installed" }
        return docker.running ? "running" : "stopped"
    }

    private var rosettaStatusText: String {
        rosettaLabel(model.rosettaStatus)
    }

    private var detailPlaceholder: some View {
        VStack(spacing: 8) {
            Text("Select a machine")
                .font(.title3)
            Text("or create one with +")
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func statusDot(color: Color) -> some View {
        Circle()
            .fill(color)
            .frame(width: 8, height: 8)
    }
}

/// Maps `StatusModel.rosettaStatus`'s raw values to a human-readable label.
/// Pulled out as a free function (rather than a private view helper) so it
/// can be unit-tested directly.
func rosettaLabel(_ status: String) -> String {
    switch status {
    case "installed": return "installed"
    case "notInstalled": return "not installed"
    case "notSupported": return "not supported"
    default: return "—"
    }
}

/// Placeholder shown while `model.onboardingNeeded` is true (daemon
/// unreachable or CLI missing). T4 replaces this with the real
/// OnboardingView first-run install flow; kept as its own small struct so
/// that swap is a self-contained diff.
struct OnboardingPlaceholder: View {
    var body: some View {
        VStack(spacing: 12) {
            Text("Umbra daemon not installed")
                .font(.title2)
                .bold()
            Text("First-run setup arrives in the next build")
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding()
    }
}
