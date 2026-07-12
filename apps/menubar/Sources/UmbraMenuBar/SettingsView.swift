import SwiftUI

// Settings scene stub, reachable via Cmd-, / the app menu on a regular
// (dock) app — see docs/research/full-app-and-dmg.md §1-2. T4 replaces the
// body with the real onboarding/daemon-management settings pane; kept in
// its own file so that swap is a self-contained diff.

struct SettingsView: View {
    @EnvironmentObject var model: StatusModel

    var body: some View {
        Text("Settings")
            .frame(width: 420, height: 300)
            .padding()
    }
}
