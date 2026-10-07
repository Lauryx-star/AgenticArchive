import itertools
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from ruamel.yaml import YAML


class SecurityGateTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        workflow = Path(__file__).resolve().parents[1] / ".github/workflows/security.yml"
        cls.jobs = YAML(typ="safe").load(workflow.read_text())["jobs"]
        cls.gate = cls.jobs["gate"]

    def run_gate(self, results):
        return subprocess.run(
            ["bash", "-e", "-o", "pipefail", "-c", self.gate["steps"][0]["run"]],
            env={**os.environ, "RESULTS": results},
            capture_output=True,
            text=True,
            timeout=5,
        ).returncode

    def test_all_scanners_are_required(self):
        self.assertEqual(self.gate["name"], "Security gate")
        self.assertEqual(self.gate["if"], "${{ always() }}")
        self.assertEqual(set(self.gate["needs"]), set(self.jobs) - {"gate"})
        self.assertEqual(len(self.gate["needs"]), 4)
        self.assertEqual(
            self.gate["steps"][0]["env"]["RESULTS"], "${{ toJSON(needs) }}"
        )

    def test_only_all_success_is_accepted(self):
        states = ["success", "failure", "cancelled", "skipped", "unexpected"]
        for results in itertools.product(states, repeat=4):
            with self.subTest(results=results):
                needs = {
                    job: {"result": result}
                    for job, result in zip(self.gate["needs"], results)
                }
                self.assertEqual(
                    self.run_gate(json.dumps(needs)) == 0,
                    all(result == "success" for result in results),
                )

    def test_invalid_or_incomplete_results_fail(self):
        for results in [
            "{}",
            "not json",
            '{"go": {"result": "success"}}',
            '{"go": {}, "static": {}, "repository": {}, "container": {}}',
        ]:
            with self.subTest(results=results):
                self.assertNotEqual(self.run_gate(results), 0)

    def test_inline_suppressions_fail_before_scanning(self):
        command = self.jobs["repository"]["steps"][-1]["run"].partition("trap ")[0]
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "Dockerfile"
            for tool in ["trivy", "tfsec"]:
                with self.subTest(tool=tool):
                    target.write_text(f"# {tool}:ignore:DS-0026\nFROM debian\n")
                    result = subprocess.run(
                        ["bash", "-e", "-o", "pipefail", "-c", command],
                        cwd=directory,
                        capture_output=True,
                        timeout=5,
                    )
                    self.assertNotEqual(result.returncode, 0)
            target.write_text("FROM debian\n")
            result = subprocess.run(
                ["bash", "-e", "-o", "pipefail", "-c", command],
                cwd=directory,
                capture_output=True,
                timeout=5,
            )
            self.assertEqual(result.returncode, 0)

    def test_logged_trivy_errors_fail(self):
        command = self.jobs["repository"]["steps"][-1]["run"]
        command = command[command.index("if grep -Eq"):]
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / "trivy-errors.log"
            for level in ["INFO", "ERROR", "FATAL"]:
                with self.subTest(level=level):
                    log.write_text(f"timestamp\t{level}\tScanner message\n")
                    result = subprocess.run(
                        ["bash", "-e", "-o", "pipefail", "-c", command],
                        env={**os.environ, "RUNNER_TEMP": directory},
                        capture_output=True,
                        timeout=5,
                    )
                    self.assertEqual(result.returncode == 0, level == "INFO")


if __name__ == "__main__":
    unittest.main()
