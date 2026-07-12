import XCTest
@testable import UmbraMenuBar

final class DashboardViewTests: XCTestCase {
    func testRosettaLabelMapsKnownStatuses() {
        XCTAssertEqual(rosettaLabel("installed"), "installed")
        XCTAssertEqual(rosettaLabel("notInstalled"), "not installed")
        XCTAssertEqual(rosettaLabel("notSupported"), "not supported")
    }

    func testRosettaLabelFallsBackToDashForUnknownOrGarbage() {
        XCTAssertEqual(rosettaLabel("unknown"), "—")
        XCTAssertEqual(rosettaLabel("garbage"), "—")
    }
}
