import SwiftUI

// "+ New Machine" sheet presented from DashboardView's toolbar. Defaults
// come from @AppStorage so repeat creations keep the last-used shape.

struct NewMachineSheet: View {
    @EnvironmentObject var model: StatusModel
    @Environment(\.dismiss) private var dismiss

    @AppStorage("defaultCPUs") private var defaultCPUs = 4
    @AppStorage("defaultMemoryGiB") private var defaultMemoryGiB = 8
    @AppStorage("defaultDiskGiB") private var defaultDiskGiB = 64

    @State private var name = ""
    @State private var cpus = 0
    @State private var memoryGiB = 0
    @State private var diskGiB = 0
    @State private var creating = false

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("New Machine")
                .font(.headline)

            Form {
                TextField("Name", text: $name)
                Stepper("CPUs: \(cpus)", value: $cpus, in: 1...16)
                Stepper("Memory: \(memoryGiB) GiB", value: $memoryGiB, in: 1...64)
                Stepper("Disk: \(diskGiB) GiB", value: $diskGiB, in: 8...512)
            }

            if let actionError = model.actionError {
                Text(actionError)
                    .font(.caption)
                    .foregroundStyle(.red)
            }

            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                Button("Create") {
                    creating = true
                    Task {
                        await model.createMachine(name, cpus: cpus, memoryGiB: memoryGiB, diskGiB: diskGiB)
                        creating = false
                        if model.actionError == nil {
                            dismiss()
                        }
                    }
                }
                .buttonStyle(.borderedProminent)
                .keyboardShortcut(.defaultAction)
                .disabled(name.isEmpty || creating)
            }
        }
        .padding()
        .frame(width: 380)
        .onAppear {
            cpus = defaultCPUs
            memoryGiB = defaultMemoryGiB
            diskGiB = defaultDiskGiB
        }
    }
}
