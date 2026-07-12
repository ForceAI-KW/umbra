// swift-tools-version:5.9
import PackageDescription

// "UmbraMenuBar" is an `.executableTarget` — Sources/UmbraMenuBar/UmbraApp.swift
// provides the `@main` entry point (M5 Task 3, the SwiftUI `MenuBarExtra` App).
let package = Package(
    name: "UmbraMenuBar",
    platforms: [.macOS(.v13)],
    targets: [
        .executableTarget(
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
