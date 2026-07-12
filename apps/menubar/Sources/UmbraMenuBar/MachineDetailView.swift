import SwiftUI

// Dashboard detail pane for a single machine: header (name + status pill), a
// stat-tile grid (CPU/Memory/Disk/IP/SSH), and start/stop, open-shell,
// delete actions. Status presentation comes from `StatusStyle` in
// Theme.swift, shared with DashboardView/MenuBarView.

struct MachineDetailView: View {
    let machine: Machine
    @EnvironmentObject var model: StatusModel
    @State private var showDeleteConfirm = false

    private var statusStyle: StatusStyle { StatusStyle(machine) }

    private let columns = [
        GridItem(.flexible()),
        GridItem(.flexible()),
        GridItem(.flexible())
    ]

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            header
            statGrid
            Spacer(minLength: 0)
            actions
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .navigationTitle(machine.name)
    }

    private var header: some View {
        HStack(spacing: 10) {
            Text(machine.name)
                .font(.title2.bold())
            StatusPill(color: statusStyle.color, text: statusStyle.label)
        }
    }

    private var statGrid: some View {
        LazyVGrid(columns: columns, spacing: 12) {
            statTile(icon: "cpu", value: "\(machine.cpus)", label: "CPU", numeric: true)
            statTile(icon: "memorychip", value: "\(machine.memoryMiB / 1024) GiB", label: "Memory", numeric: true)
            statTile(icon: "internaldrive", value: "\(machine.diskGiB) GiB", label: "Disk", numeric: true)
            statTile(icon: "network", value: machine.ip ?? "—", label: "IP", numeric: false)
            statTile(icon: "terminal", value: machine.sshPort.map(String.init) ?? "—", label: "SSH", numeric: true)
        }
    }

    private func statTile(icon: String, value: String, label: String, numeric: Bool) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Image(systemName: icon)
                .foregroundStyle(.secondary)
            Text(value)
                .font(numeric ? .body.monospacedDigit() : .body)
            Text(label)
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding()
        .background(Color.primary.opacity(0.04), in: RoundedRectangle(cornerRadius: 10))
    }

    private var actions: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 12) {
                Button {
                    Task { await model.toggleMachine(machine) }
                } label: {
                    if model.busy.contains(machine.name) {
                        ProgressView().scaleEffect(0.5)
                    } else {
                        Text(machine.state == .running ? "Stop" : "Start")
                    }
                }
                .buttonStyle(.borderedProminent)

                Button("Open Shell") {
                    model.openShell(machine)
                }
                .buttonStyle(.bordered)
                .disabled(!(machine.state == .running && machine.sshPort != nil))

                Button("Delete") {
                    showDeleteConfirm = true
                }
                .buttonStyle(.bordered)
                .tint(.red)
                .disabled(machine.state != .stopped)
                .confirmationDialog(
                    "Delete \(machine.name)?",
                    isPresented: $showDeleteConfirm,
                    titleVisibility: .visible
                ) {
                    Button("Delete", role: .destructive) {
                        Task { await model.deleteMachine(machine.name) }
                    }
                    Button("Cancel", role: .cancel) {}
                }
            }

            if machine.state != .stopped {
                Text("Stop the machine before deleting")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            if let actionError = model.actionError {
                Text(actionError)
                    .font(.caption)
                    .foregroundStyle(.red)
            }
        }
    }
}
