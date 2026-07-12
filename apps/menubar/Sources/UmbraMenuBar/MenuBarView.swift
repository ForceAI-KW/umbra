import SwiftUI
import AppKit

// The MenuBarExtra `.window`-style popover content: status header, machine
// list with per-row start/stop + shell actions, docker toggle, quit footer.
// Status presentation comes from `StatusStyle`/`daemonDotColor`/`StatusPill`
// in Theme.swift, shared with DashboardView/MachineDetailView. See
// docs/research/menubar-app.md §1 (.window style), §7 (status dot).

struct MenuBarView: View {
    @EnvironmentObject var model: StatusModel

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header

            if model.cliMissing {
                Text("umbra CLI not found — run `make build` or install it")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .padding(.horizontal)
                    .padding(.vertical, 12)
            } else {
                machineList
                Divider()
                dockerSection
            }

            Divider()
            footer
        }
        .frame(width: 320)
        .onAppear { model.surfaceAppeared() }
        .onDisappear { model.surfaceDisappeared() }
    }

    private var header: some View {
        HStack(spacing: 6) {
            UmbraMark(size: 16)
            Text("Umbra").bold()
            Spacer()
            StatusPill(color: daemonColor, text: daemonText)
        }
        .padding()
    }

    private var daemonColor: Color {
        daemonDotColor(daemon: model.status?.daemon, cliMissing: model.cliMissing)
    }

    private var daemonText: String {
        if model.cliMissing { return "—" }
        switch model.status?.daemon {
        case "up": return "up"
        case "down": return "down"
        default: return "—"
        }
    }

    private var machineList: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(model.status?.machines ?? []) { machine in
                machineRow(machine)
            }
        }
        .padding(.horizontal)
        .padding(.vertical, 8)
    }

    private func machineRow(_ machine: Machine) -> some View {
        let s = StatusStyle(machine)
        return HStack {
            Image(systemName: s.symbol)
                .foregroundStyle(s.color)
            VStack(alignment: .leading, spacing: 2) {
                Text(machine.name)
                Text("\(machine.cpus) CPU · \(machine.memoryMiB / 1024) GiB")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            StatusPill(color: s.color, text: s.label)

            Button {
                Task { await model.toggleMachine(machine) }
            } label: {
                if model.busy.contains(machine.name) {
                    ProgressView().scaleEffect(0.5)
                } else {
                    Image(systemName: machine.state == .running ? "stop.fill" : "play.fill")
                }
            }
            .buttonStyle(.borderless)

            Button {
                model.openShell(machine)
            } label: {
                Image(systemName: "terminal")
            }
            .buttonStyle(.borderless)
            .disabled(!(machine.state == .running && machine.sshPort != nil))
        }
    }

    private var dockerSection: some View {
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
        .padding()
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

    private var footer: some View {
        HStack {
            Spacer()
            Button("Quit Umbra") {
                NSApplication.shared.terminate(nil)
            }
            .buttonStyle(.borderless)
        }
        .padding()
    }
}
