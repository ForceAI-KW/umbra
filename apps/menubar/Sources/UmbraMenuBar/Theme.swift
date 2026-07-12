import SwiftUI

// The app's design system: brand palette, one source of truth for machine
// status presentation (color + label + SF Symbol — previously triplicated
// across MenuBarView / DashboardView / MachineDetailView), and the `UmbraMark`
// eclipse glyph that echoes the app icon. Keep every status color/label change
// here so the three surfaces never drift.

extension Color {
    /// Force-brand orange — the app's accent (buttons, selection, brand marks).
    /// Semantic status colors (green/red/yellow) stay separate and are NOT
    /// replaced by the accent, so "running" reads unambiguously.
    static let umbraAccent = Color(red: 1.0, green: 0.431, blue: 0.004)      // #ff6e01
    static let umbraAccentBright = Color(red: 1.0, green: 0.541, blue: 0.180) // #ff8a2e

    static let umbraRunning = Color(red: 0.30, green: 0.78, blue: 0.42)
    static let umbraPending = Color(red: 0.98, green: 0.72, blue: 0.18)
    static let umbraStopped = Color(red: 0.55, green: 0.56, blue: 0.60)
    static let umbraError = Color(red: 0.94, green: 0.35, blue: 0.33)
}

/// One-stop presentation for a machine's runtime state: the dot/pill color,
/// the human label ("crashed*" when a stop wasn't confirmed), and an SF Symbol.
/// Built from a `Machine` so all three views render identically.
struct StatusStyle {
    let color: Color
    let label: String
    let symbol: String

    init(_ machine: Machine) {
        if machine.zombie == true {
            self = StatusStyle(color: .umbraError, label: "crashed*", symbol: "exclamationmark.triangle.fill")
            return
        }
        switch machine.state {
        case .running:
            self = StatusStyle(color: .umbraRunning, label: "running", symbol: "bolt.fill")
        case .starting:
            self = StatusStyle(color: .umbraPending, label: "starting", symbol: "arrow.up.circle")
        case .stopping:
            self = StatusStyle(color: .umbraPending, label: "stopping", symbol: "arrow.down.circle")
        case .stopped:
            self = StatusStyle(color: .umbraStopped, label: "stopped", symbol: "moon.zzz.fill")
        case .crashed:
            self = StatusStyle(color: .umbraError, label: "crashed", symbol: "exclamationmark.triangle.fill")
        }
    }

    private init(color: Color, label: String, symbol: String) {
        self.color = color
        self.label = label
        self.symbol = symbol
    }
}

/// Daemon reachability → dot color, shared by the sidebar + menu-bar headers.
func daemonDotColor(daemon: String?, cliMissing: Bool) -> Color {
    if cliMissing { return .umbraStopped }
    switch daemon {
    case "up": return .umbraRunning
    case "down": return .umbraError
    default: return .umbraStopped
    }
}

/// A small colored capsule showing a status dot + label — the standard way to
/// render machine/daemon state across the app. Holds its intrinsic width so it
/// never wraps its label inside a narrow sidebar column.
struct StatusPill: View {
    let color: Color
    let text: String
    var body: some View {
        HStack(spacing: 5) {
            Circle().fill(color).frame(width: 6, height: 6)
            Text(text)
                .font(.caption.weight(.medium))
                .foregroundStyle(color)
                .lineLimit(1)
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .background(color.opacity(0.16), in: Capsule())
        .overlay(Capsule().strokeBorder(color.opacity(0.28), lineWidth: 0.5))
        .fixedSize()
    }
}

/// A raised surface: subtle fill + hairline border + rounded corners. The
/// standard container for stat tiles and grouped content, giving the dark UI
/// depth without heavy shadows.
struct UmbraCard<Content: View>: View {
    var padding: CGFloat = 14
    @ViewBuilder var content: Content
    var body: some View {
        content
            .padding(padding)
            .background(Color.primary.opacity(0.045), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 12, style: .continuous)
                    .strokeBorder(Color.primary.opacity(0.09), lineWidth: 1)
            )
    }
}

/// An uppercase section label for grouping (e.g. "MACHINES").
struct SectionLabel: View {
    let text: String
    var body: some View {
        Text(text.uppercased())
            .font(.caption2.weight(.semibold))
            .foregroundStyle(.tertiary)
            .tracking(0.6)
    }
}

/// The Umbra "eclipse" mark — a black core ringed by an orange corona, echoing
/// the app icon. Used in the sidebar header and the onboarding hero.
struct UmbraMark: View {
    var size: CGFloat = 22
    var body: some View {
        ZStack {
            Circle()
                .strokeBorder(
                    LinearGradient(
                        colors: [.umbraAccentBright, .umbraAccent, Color(red: 0.85, green: 0.31, blue: 0.0)],
                        startPoint: .top, endPoint: .bottom
                    ),
                    lineWidth: max(1.5, size * 0.11)
                )
            Circle()
                .fill(Color.black)
                .padding(size * 0.17)
        }
        .frame(width: size, height: size)
        .shadow(color: .umbraAccent.opacity(0.55), radius: size * 0.16)
    }
}
