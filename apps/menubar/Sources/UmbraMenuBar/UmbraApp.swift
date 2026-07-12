import SwiftUI

// App entry point. `.window` style (not `.menu`) so the popover can host
// arbitrary SwiftUI content (List, buttons, colored dots) rather than being
// restricted to native menu chrome. See docs/research/menubar-app.md §1.

@main
struct UmbraApp: App {
    @StateObject private var model = StatusModel()

    var body: some Scene {
        MenuBarExtra("Umbra", systemImage: "cube.fill") {
            MenuBarView()
                .environmentObject(model)
        }
        .menuBarExtraStyle(.window)
    }
}
