import SwiftUI
import AppKit

// Main dashboard window: a NavigationSplitView (machine list sidebar +
// machine detail) once the daemon is reachable, or the onboarding flow while
// first-run setup is needed. Shares `StatusModel` with the MenuBarExtra popover
// via `.environmentObject`. Status presentation (dot color / label / symbol)
// comes from `StatusStyle`/`daemonDotColor` in Theme.swift — the single source
// of truth shared with MenuBarView/MachineDetailView.

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
        .frame(minWidth: 760, minHeight: 500)
        .onAppear { model.surfaceAppeared() }
        .onDisappear { model.surfaceDisappeared() }
        // Auto-select the first machine so a populated dashboard opens on
        // detail rather than the empty state, and keep the selection valid
        // when the selected machine is deleted out from under us.
        .onChange(of: machines.map(\.name)) { names in
            if let sel = selected, !names.contains(sel) { selected = nil }
            if selected == nil { selected = names.first }
        }
        .sheet(isPresented: $showNewMachine) {
            NewMachineSheet()
                .environmentObject(model)
        }
    }

    private var machines: [Machine] { model.status?.machines ?? [] }

    private var splitView: some View {
        NavigationSplitView {
            sidebar
                .navigationSplitViewColumnWidth(min: 236, ideal: 248, max: 320)
        } detail: {
            Group {
                if let machine = selectedMachine {
                    MachineDetailView(machine: machine)
                } else {
                    detailPlaceholder
                }
            }
            .toolbar {
                ToolbarItem(placement: .primaryAction) {
                    Button {
                        showNewMachine = true
                    } label: {
                        Label("New Machine", systemImage: "plus")
                    }
                    .keyboardShortcut("n", modifiers: .command)
                    .help("Create a new Linux machine (⌘N)")
                }
            }
        }
    }

    private var selectedMachine: Machine? {
        machines.first { $0.name == selected }
    }

    // MARK: - Sidebar

    private var sidebar: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Divider().opacity(0.5)
            machineList
            Divider().opacity(0.5)
            footer
        }
    }

    private var header: some View {
        HStack(spacing: 10) {
            UmbraMark(size: 26)
            VStack(alignment: .leading, spacing: 2) {
                Text("Umbra")
                    .font(.headline)
                StatusPill(color: daemonColor, text: daemonStateText)
            }
            Spacer(minLength: 0)
            settingsButton
        }
        .padding(.horizontal, 14)
        .padding(.top, 14)
        .padding(.bottom, 12)
    }

    private var machineList: some View {
        Group {
            if machines.isEmpty {
                VStack(alignment: .leading, spacing: 10) {
                    SectionLabel(text: "Machines")
                    Spacer()
                    HStack {
                        Spacer()
                        VStack(spacing: 6) {
                            Image(systemName: "tray")
                                .font(.title2)
                                .foregroundStyle(.tertiary)
                            Text("No machines yet")
                                .font(.callout)
                                .foregroundStyle(.secondary)
                        }
                        Spacer()
                    }
                    Spacer()
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 12)
            } else {
                List(selection: $selected) {
                    Section {
                        ForEach(machines) { machine in
                            machineRow(machine).tag(machine.name)
                        }
                    } header: {
                        SectionLabel(text: "Machines")
                    }
                }
                .listStyle(.sidebar)
                .scrollContentBackground(.hidden)
            }
        }
        .frame(maxHeight: .infinity)
    }

    private func machineRow(_ machine: Machine) -> some View {
        let s = StatusStyle(machine)
        return HStack(spacing: 10) {
            Image(systemName: s.symbol)
                .font(.body)
                .foregroundStyle(s.color)
                .frame(width: 18)
            Text(machine.name)
                .font(.body)
                .lineLimit(1)
            Spacer(minLength: 8)
            StatusPill(color: s.color, text: s.label)
        }
        .padding(.vertical, 4)
    }

    private var footer: some View {
        VStack(spacing: 8) {
            footerRow(icon: "shippingbox.fill", label: "Docker") {
                StatusPill(color: dockerColor, text: dockerStatusText)
                if model.status?.docker?.installed == true {
                    Button {
                        Task { await model.toggleDocker() }
                    } label: {
                        if model.busy.contains("docker") {
                            ProgressView().controlSize(.small)
                        } else {
                            Image(systemName: model.status?.docker?.running == true ? "stop.fill" : "play.fill")
                        }
                    }
                    .buttonStyle(.borderless)
                } else {
                    Button {
                        Task { await model.installDocker() }
                    } label: {
                        if model.busy.contains("docker") {
                            HStack(spacing: 5) {
                                ProgressView().controlSize(.small)
                                Text("Installing…").font(.caption)
                            }
                        } else {
                            Text("Install")
                        }
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.small)
                    .disabled(model.busy.contains("docker") || model.cliMissing)
                    .help("Provision and start the Docker VM")
                }
            }
            footerRow(icon: "cpu", label: "Rosetta") {
                Text(rosettaStatusText)
                    .font(.caption.weight(.medium))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
        }
        .padding(14)
    }

    private func footerRow<Trailing: View>(icon: String, label: String, @ViewBuilder trailing: () -> Trailing) -> some View {
        HStack(spacing: 8) {
            Image(systemName: icon)
                .font(.caption)
                .foregroundStyle(.secondary)
                .frame(width: 16)
            Text(label)
                .font(.callout)
                .lineLimit(1)
            Spacer(minLength: 8)
            trailing()
        }
    }

    // MARK: - Detail placeholder (empty state)

    private var detailPlaceholder: some View {
        VStack(spacing: 18) {
            UmbraMark(size: 72)
            VStack(spacing: 6) {
                Text("No machine selected")
                    .font(.title2.weight(.semibold))
                Text(machines.isEmpty
                     ? "Create your first Linux machine to get started."
                     : "Pick a machine on the left to see its details.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
            }
            Button {
                showNewMachine = true
            } label: {
                Label("New Machine", systemImage: "plus")
                    .padding(.horizontal, 6)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
        }
        .padding(40)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    // MARK: - Settings button (macOS 13 fallback for SettingsLink)

    @ViewBuilder
    private var settingsButton: some View {
        if #available(macOS 14.0, *) {
            SettingsLink {
                Image(systemName: "gearshape")
                    .font(.body)
                    .foregroundStyle(.secondary)
            }
            .buttonStyle(.borderless)
        } else {
            Button {
                NSApp.sendAction(Selector(("showSettingsWindow:")), to: nil, from: nil)
            } label: {
                Image(systemName: "gearshape")
                    .font(.body)
                    .foregroundStyle(.secondary)
            }
            .buttonStyle(.borderless)
        }
    }

    // MARK: - Derived state

    private var daemonColor: Color {
        daemonDotColor(daemon: model.status?.daemon, cliMissing: model.cliMissing)
    }

    private var daemonStateText: String {
        if model.cliMissing { return "daemon —" }
        switch model.status?.daemon {
        case "up": return "daemon up"
        case "down": return "daemon down"
        default: return "daemon —"
        }
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
