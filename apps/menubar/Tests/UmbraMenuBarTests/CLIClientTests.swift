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

    // MARK: - createArgs

    func testCreateArgsBuildsExactArray() {
        let args = createArgs(name: "dev", cpus: 4, memoryGiB: 8, diskGiB: 60)
        XCTAssertEqual(args, ["create", "dev", "--cpus", "4", "--memory-gib", "8", "--disk-gib", "60"])
    }

    // MARK: - parseRosettaStatus

    func testParseRosettaStatusInstalled() {
        XCTAssertEqual(parseRosettaStatus("Rosetta: installed"), "installed")
    }

    func testParseRosettaStatusNotInstalled() {
        XCTAssertEqual(parseRosettaStatus("Rosetta: not installed"), "notInstalled")
    }

    func testParseRosettaStatusNotSupported() {
        XCTAssertEqual(parseRosettaStatus("Rosetta: not supported"), "notSupported")
    }

    func testParseRosettaStatusGarbageIsUnknown() {
        XCTAssertEqual(parseRosettaStatus("nonsense output"), "unknown")
    }

    // MARK: - shellQuote

    func testShellQuoteEscapesEmbeddedSingleQuote() {
        XCTAssertEqual(shellQuote("it's"), "'it'\\''s'")
    }

    // MARK: - adminInstallScript

    func testAdminInstallScriptTightensUmbradSigningAndLeavesUmbraPlain() {
        let entitlements = "/bundle/vz.entitlements"
        let script = adminInstallScript(umbra: "/bundle/umbra", umbrad: "/bundle/umbrad", entitlements: entitlements)

        XCTAssertTrue(script.contains("cp"))
        XCTAssertTrue(script.contains("with administrator privileges"))

        // umbrad must be re-signed WITH its virtualization entitlements — a
        // regression that drops `--entitlements` here would silently break
        // Virtualization.framework at runtime. Assert the exact substring
        // (not independent .contains checks) so this test actually fails
        // if `--entitlements` is ever removed from umbrad's codesign call.
        XCTAssertTrue(script.contains("codesign --force --entitlements '\(entitlements)' --sign - /usr/local/bin/umbrad"))

        // umbra itself must be signed plain (no entitlements): assert the
        // exact trailing codesign line, and that --entitlements never
        // precedes umbra's own sign step.
        XCTAssertTrue(script.contains("codesign --force --sign - /usr/local/bin/umbra\""))
        XCTAssertFalse(script.contains("--entitlements '\(entitlements)' --sign - /usr/local/bin/umbra\""))
    }
}
