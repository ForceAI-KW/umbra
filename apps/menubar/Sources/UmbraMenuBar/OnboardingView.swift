import SwiftUI

// First-run onboarding, shown by DashboardView while `model.onboardingNeeded`
// (CLI unreachable, or reachable but daemon down — §3,
// docs/research/full-app-and-dmg.md). "Install Umbra" drives
// `StatusModel.installDaemon()`, which resolves the bundled `umbra`/`umbrad`/
// entitlements via Bundle.main, copies them to /usr/local/bin (prompting for
// admin only if needed), and registers the LaunchAgent — same install.sh
// discipline (mkdir -p, re-sign umbrad with its vz entitlement) described
// there, just triggered from the app instead of the shell script. On
// success `refresh()` flips `onboardingNeeded` false and DashboardView swaps
// to the split view on its own.

struct OnboardingView: View {
    @EnvironmentObject var model: StatusModel

    var body: some View {
        VStack(spacing: 16) {
            Image(systemName: "cube.fill")
                .font(.system(size: 48))
                .foregroundStyle(.secondary)

            Text("Welcome to Umbra")
                .font(.title2)
                .bold()

            Text("Umbra runs Linux VMs and Docker containers on Apple Silicon using Virtualization.framework. Install the background daemon to get started.")
                .multilineTextAlignment(.center)
                .foregroundStyle(.secondary)

            Text("First run: the first time umbrad boots a VM, macOS shows a one-time Virtualization permission prompt — approve it.")
                .font(.caption)
                .multilineTextAlignment(.center)
                .foregroundStyle(.secondary)

            VStack(spacing: 8) {
                Button("Install Umbra") {
                    Task { await model.installDaemon() }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(model.installing)

                if model.installing {
                    HStack(spacing: 6) {
                        ProgressView()
                            .scaleEffect(0.7)
                        Text("Installing…")
                    }
                    .foregroundStyle(.secondary)
                }
            }

            if let installError = model.installError {
                Text(installError)
                    .font(.caption)
                    .foregroundStyle(.red)
                    .multilineTextAlignment(.center)
            }
        }
        .padding(24)
        .frame(maxWidth: 460)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}
