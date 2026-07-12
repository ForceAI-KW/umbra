import SwiftUI
import AppKit

// The MenuBarExtra `.window`-style popover content: status header, machine
// list with per-row start/stop + shell actions, docker toggle, quit footer.
// See docs/research/menubar-app.md §1 (.window style), §7 (status dot).

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
        .onAppear { model.startPolling() }
        .onDisappear { model.stopPolling() }
    }

    private var header: some View {
        HStack {
            statusDot(color: headerDotColor)
            Text("Umbra").bold()
            Spacer()
        }
        .padding()
    }

    private var headerDotColor: Color {
        if model.cliMissing { return .gray }
        switch model.status?.daemon {
        case "up": return .green
        case "down": return .red
        default: return .gray
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
        HStack {
            statusDot(color: machineDotColor(machine))
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 4) {
                    Text(machine.name)
                    Text(stateLabel(machine))
                        .foregroundStyle(.secondary)
                }
                Text("\(machine.cpus) CPU · \(machine.memoryMiB / 1024) GiB")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()

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

    private var dockerSection: some View {
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
        .padding()
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

    private func statusDot(color: Color) -> some View {
        Circle()
            .fill(color)
            .frame(width: 8, height: 8)
    }
}
