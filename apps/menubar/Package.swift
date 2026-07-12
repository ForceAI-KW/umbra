// swift-tools-version:5.9
import PackageDescription

// NOTE (M5 Task 1): "UmbraMenuBar" is a `.target` (library), not an
// `.executableTarget`, for now. An executableTarget needs a main entry
// point (an `@main` App or main.swift) to link; that lands in Task 3 when
// the SwiftUI `MenuBarExtra` App is added. A library target compiles and
// is testable without one. Task 3 should switch this to `.executableTarget`
// once Sources/UmbraMenuBar/App.swift (the @main entry) exists.
let package = Package(
    name: "UmbraMenuBar",
    platforms: [.macOS(.v13)],
    targets: [
        .target(
            name: "UmbraMenuBar",
            path: "Sources/UmbraMenuBar"
        ),
        .testTarget(
            name: "UmbraMenuBarTests",
            dependencies: ["UmbraMenuBar"],
            path: "Tests/UmbraMenuBarTests"
        ),
    ]
)
