import XCTest
@testable import UmbraMenuBar

final class CLIClientTests: XCTestCase {
    // MARK: - appleScriptEscape

    func testAppleScriptEscapeEscapesBackslashBeforeQuote() {
        let input = "a\"b\\c"
        let escaped = appleScriptEscape(input)
        XCTAssertEqual(escaped, "a\\\"b\\\\c")
    }

    // MARK: - openShellScript

    func testOpenShellScriptContainsUmbraShellCommand() {
        let script = openShellScript(machineName: "dev")
        XCTAssertTrue(script.contains("do script \"umbra shell dev\""))
    }

    func testOpenShellScriptEscapesQuoteInMachineName() {
        let script = openShellScript(machineName: "de\"v")
        XCTAssertTrue(script.contains("umbra shell de\\\"v"))
    }

    // MARK: - resolveCLIPath

    func testResolveCLIPathReturnsFirstExistingCandidate() throws {
        let tempFile = FileManager.default.temporaryDirectory
            .appendingPathComponent("umbra-cliclient-test-\(UUID().uuidString)")
        FileManager.default.createFile(atPath: tempFile.path, contents: Data())
        defer { try? FileManager.default.removeItem(at: tempFile) }

        let resolved = resolveCLIPath(candidates: [tempFile.path, "/nonexistent/umbra"])
        XCTAssertEqual(resolved, tempFile.path)
    }

    func testResolveCLIPathReturnsNilWhenNoneExist() {
        let resolved = resolveCLIPath(candidates: ["/nope1", "/nope2"])
        XCTAssertNil(resolved)
    }
}
