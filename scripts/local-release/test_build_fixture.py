"""The auxiliary fixture approval requires fixture tests that exist."""

import re
import unittest

import build_fixture


class BuildFixtureTests(unittest.TestCase):
    def test_required_tests_are_defined_in_the_fixture_tests(self):
        source = (build_fixture.ROOT / "scripts/local-test/fixture_test.go").read_text(encoding="utf-8")
        defined = set(re.findall(r"^func (Test\w+)\(t \*testing\.T\)", source, re.M))
        self.assertLessEqual(build_fixture.REQUIRED_TESTS, defined)


if __name__ == "__main__":
    unittest.main()
