import unittest
from types import SimpleNamespace
from unittest.mock import patch

from scripts.local_operations import WorkflowError
from signoff import crash_backend


class CrashTargetSafetyTest(unittest.TestCase):
    def test_foreign_project_and_invalid_pid_never_receive_a_signal(self):
        for project, pid, running in (("stock-ai-t10-cert", 42, True),
                                      ("stock-ai-local-release", -1, True),
                                      ("stock-ai-local-release", 1, True),
                                      ("stock-ai-local-release", 42, False)):
            with self.subTest(project=project, pid=pid, running=running):
                before = {"Config": {"Labels": {"com.docker.compose.project": project}},
                          "State": {"Running": running, "Pid": pid}}
                operator = SimpleNamespace(container=lambda service: before)
                with patch("signoff.run") as command:
                    with self.assertRaises(WorkflowError):
                        crash_backend(operator, {})
                    command.assert_not_called()


if __name__ == "__main__":
    unittest.main()
