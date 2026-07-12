import SwiftUI
import AppKit

// Main dashboard window: a NavigationSplitView (machine list sidebar +
// machine detail) once the daemon is reachable, or a placeholder while
// first-run onboarding is needed. Shares `StatusModel` with the
// MenuBarExtra popover via `.environmentObject` (docs/research/
// full-app-and-dmg.md §1). Status presentation (dot color / label / symbol)
// comes from `StatusStyle`/`daemonDotColor` in Theme.swift — the single
// source of truth shared with MenuBarView/MachineDetailView.

struct DashboardView: View {
    @EnvironmentObject var model: StatusModel
    @State private var selected: String?
    @State private var showNewMachine = false

    var body: some View {
        Group {
            if model.onboardingNeeded {
                OnboardingView()
            } else {
                splitView
            }
        }
        .frame(minWidth: 700, minHeight: 460)
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
        HStack(spacing: 8) {
            UmbraMark(size: 22)
            VStack(alignment: .leading, spacing: 1) {
                Text("Umbra")
                    .font(.headline)
                Text(daemonStateText)
                    .font(.caption2)
                    .foregroundStyle(daemonDotColor(daemon: model.status?.daemon, cliMissing: model.cliMissing))
            }
            Spacer()
            settingsButton
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
    }

    private var daemonStateText: String {
        if model.cliMissing { return "daemon —" }
        switch model.status?.daemon {
        case "up": return "daemon up"
        case "down": return "daemon down"
        default: return "daemon —"
        }
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

    private func machineRow(_ machine: Machine) -> some View {
        let s = StatusStyle(machine)
        return HStack {
            Image(systemName: s.symbol)
                .font(.body)
                .foregroundStyle(s.color)
            Text(machine.name)
                .font(.body)
            Spacer()
            StatusPill(color: s.color, text: s.label)
        }
        .padding(.vertical, 4)
    }

    private var footer: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Image(systemName: "shippingbox.fill")
                    .foregroundStyle(.secondary)
                Text("Docker")
                Spacer()
                StatusPill(color: dockerColor, text: dockerStatusText)
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
            HStack {
                Image(systemName: "cpu")
                    .foregroundStyle(.secondary)
                Text("Rosetta")
                Spacer()
                Text(rosettaStatusText)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(12)
    }

    private var dockerColor: Color {
        guard let docker = model.status?.docker, docker.installed else { return .umbraStopped }
        return docker.running ? .umbraRunning : .umbraStopped
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
        VStack(spacing: 10) {
            UmbraMark(size: 44)
            Text("No machine selected")
                .font(.title3)
            Text("Pick a machine on the left, or create one with +")
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
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
