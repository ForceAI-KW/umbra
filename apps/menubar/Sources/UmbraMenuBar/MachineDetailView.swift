import SwiftUI

// Dashboard detail pane for a single machine: header (name + state),
// IP/ssh/cpu/mem/disk stat row, start/stop, open-shell, and delete.
// Reuses DashboardView's dot-color / state-label conventions.

struct MachineDetailView: View {
    let machine: Machine
    @EnvironmentObject var model: StatusModel
    @State private var showDeleteConfirm = false

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            header
            statRow
            Spacer(minLength: 0)
            actions
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .navigationTitle(machine.name)
    }

    private var header: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text(machine.name)
                .font(.title2)
                .bold()
            Text(stateLabel)
                .foregroundStyle(.secondary)
        }
    }

    private var stateLabel: String {
        machine.zombie == true ? "crashed*" : machine.state.rawValue
    }

    private var statRow: some View {
        HStack(spacing: 24) {
            stat("IP", machine.ip ?? "—")
            stat("SSH Port", machine.sshPort.map(String.init) ?? "—")
            stat("CPU", "\(machine.cpus) CPU")
            stat("RAM", "\(machine.memoryMiB / 1024) GiB")
            stat("Disk", "\(machine.diskGiB) GiB")
        }
    }

    private func stat(_ label: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label)
                .font(.caption)
                .foregroundStyle(.secondary)
            Text(value)
        }
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

                Button("Open Shell") {
                    model.openShell(machine)
                }
                .disabled(!(machine.state == .running && machine.sshPort != nil))

                Button("Delete") {
                    showDeleteConfirm = true
                }
                .foregroundStyle(.red)
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
        }
    }
}
