import SwiftUI

// App entry point. Umbra is a regular dock app (LSUIElement=NO, see
// Info.plist) with a main dashboard Window, a Settings scene, and the
// existing MenuBarExtra convenience surface — all three scenes share one
// `StatusModel` via `.environmentObject`. See
// docs/research/full-app-and-dmg.md §1-2 (scene composition,
// LSUIElement=NO) and docs/research/menubar-app.md §1 (`.window` style for
// the menu bar popover).

@main
struct UmbraApp: App {
    @StateObject private var model = StatusModel()

    var body: some Scene {
        Window("Umbra", id: "main") {
            DashboardView()
                .environmentObject(model)
                .tint(.umbraAccent)
        }

        Settings {
            SettingsView()
                .environmentObject(model)
                .tint(.umbraAccent)
        }

        MenuBarExtra("Umbra", systemImage: "cube.fill") {
            MenuBarView()
                .environmentObject(model)
                .tint(.umbraAccent)
        }
        .menuBarExtraStyle(.window)
    }
}
