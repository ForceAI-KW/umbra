import SwiftUI

// Dashboard detail pane for a single machine: a header card (name + status +
// actions), a stat-tile grid (CPU/Memory/Disk/IP/SSH), and the delete flow.
// Status presentation comes from `StatusStyle` in Theme.swift, shared with
// DashboardView/MenuBarView.

struct MachineDetailView: View {
    let machine: Machine
    @EnvironmentObject var model: StatusModel
    @State private var showDeleteConfirm = false

    private var statusStyle: StatusStyle { StatusStyle(machine) }

    private let columns = [
        GridItem(.flexible(), spacing: 12),
        GridItem(.flexible(), spacing: 12),
        GridItem(.flexible(), spacing: 12)
    ]

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                headerCard
                SectionLabel(text: "Resources")
                statGrid
                if machine.state != .stopped {
                    Label("Stop the machine before deleting it.", systemImage: "info.circle")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                if let actionError = model.actionError {
                    Label(actionError, systemImage: "exclamationmark.triangle.fill")
                        .font(.caption)
                        .foregroundStyle(.red)
                }
            }
            .padding(20)
            .frame(maxWidth: .infinity, alignment: .topLeading)
        }
        .navigationTitle(machine.name)
    }

    // MARK: - Header

    private var headerCard: some View {
        UmbraCard(padding: 18) {
            HStack(alignment: .center, spacing: 14) {
                Image(systemName: statusStyle.symbol)
                    .font(.system(size: 28))
                    .foregroundStyle(statusStyle.color)
                    .frame(width: 44, height: 44)
                    .background(statusStyle.color.opacity(0.14), in: RoundedRectangle(cornerRadius: 10, style: .continuous))

                VStack(alignment: .leading, spacing: 5) {
                    Text(machine.name)
                        .font(.title2.weight(.semibold))
                        .lineLimit(1)
                    StatusPill(color: statusStyle.color, text: statusStyle.label)
                }

                Spacer(minLength: 12)

                actionButtons
            }
        }
    }

    private var actionButtons: some View {
        HStack(spacing: 10) {
            Button {
                Task { await model.toggleMachine(machine) }
            } label: {
                if model.busy.contains(machine.name) {
                    ProgressView().controlSize(.small)
                        .frame(width: 54)
                } else {
                    Label(machine.state == .running ? "Stop" : "Start",
                          systemImage: machine.state == .running ? "stop.fill" : "play.fill")
                }
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .disabled(model.busy.contains(machine.name))

            Button {
                model.openShell(machine)
            } label: {
                Label("Shell", systemImage: "terminal")
            }
            .buttonStyle(.bordered)
            .controlSize(.large)
            .disabled(!(machine.state == .running && machine.sshPort != nil))

            Button(role: .destructive) {
                showDeleteConfirm = true
            } label: {
                Image(systemName: "trash")
            }
            .buttonStyle(.bordered)
            .controlSize(.large)
            .tint(.red)
            .disabled(machine.state != .stopped)
            .help("Delete machine")
            .confirmationDialog(
                "Delete \(machine.name)?",
                isPresented: $showDeleteConfirm,
                titleVisibility: .visible
            ) {
                Button("Delete", role: .destructive) {
                    Task { await model.deleteMachine(machine.name) }
                }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("This permanently removes the machine and its disk.")
            }
        }
    }

    // MARK: - Stat grid

    private var statGrid: some View {
        LazyVGrid(columns: columns, spacing: 12) {
            statTile(icon: "cpu", value: "\(machine.cpus)", label: "vCPU", mono: true)
            statTile(icon: "memorychip", value: "\(machine.memoryMiB / 1024)", unit: "GiB", label: "Memory", mono: true)
            statTile(icon: "internaldrive", value: "\(machine.diskGiB)", unit: "GiB", label: "Disk", mono: true)
            statTile(icon: "network", value: machine.ip ?? "—", label: "IP address", mono: true)
            statTile(icon: "terminal", value: machine.sshPort.map(String.init) ?? "—", label: "SSH port", mono: true)
            statTile(icon: "shippingbox", value: imageLabel, label: "Image", mono: false)
        }
    }

    private var imageLabel: String {
        guard let image = machine.image, !image.isEmpty else { return "ubuntu" }
        return image
    }

    private func statTile(icon: String, value: String, unit: String? = nil, label: String, mono: Bool) -> some View {
        UmbraCard {
            VStack(alignment: .leading, spacing: 10) {
                Image(systemName: icon)
                    .font(.system(size: 15))
                    .foregroundStyle(Color.umbraAccent)
                HStack(alignment: .firstTextBaseline, spacing: 4) {
                    Text(value)
                        .font(mono ? .title3.weight(.semibold).monospacedDigit() : .title3.weight(.semibold))
                        .lineLimit(1)
                        .minimumScaleFactor(0.6)
                    if let unit {
                        Text(unit)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                Text(label)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}
