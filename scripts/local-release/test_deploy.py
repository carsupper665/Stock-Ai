import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import deploy
import prepare
from scripts.local_operations import WorkflowError


class DeploymentGateTest(unittest.TestCase):
    def test_packaged_operator_accepts_its_persistent_deployment_directory(self):
        with tempfile.TemporaryDirectory() as temporary:
            bundle = Path(temporary) / "deployment"
            bundle.mkdir()
            with patch.object(prepare, "ROOT", bundle):
                self.assertEqual(deploy.persistent(bundle), bundle)
                self.assertEqual(prepare.persistent_runtime(bundle), bundle)
            (bundle / ".git").write_text("gitdir: /tmp/disposable-checkout")
            with patch.object(prepare, "ROOT", bundle):
                with self.assertRaises(WorkflowError):
                    deploy.persistent(bundle)
                with self.assertRaises(WorkflowError):
                    prepare.persistent_runtime(bundle)

    def test_disposable_runtime_is_rejected_before_secret_writes(self):
        for path in (prepare.ROOT / ".local-release", Path("/tmp/profiles/a/worktrees/proj/release")):
            with self.subTest(path=path), patch.object(prepare.material, "_secure_directory") as write:
                with self.assertRaises(WorkflowError):
                    prepare.prepare(runtime=path)
                write.assert_not_called()

    def test_resolved_symlink_cannot_hide_worktree(self):
        with tempfile.TemporaryDirectory() as temporary:
            link = Path(temporary) / "runtime"
            link.symlink_to(prepare.ROOT)
            with self.assertRaises(WorkflowError):
                deploy.persistent(link)

    def test_occupied_port_requires_existing_release_nginx(self):
        for service in ("backend", "nginx"):
            incumbent = {"Config": {"Labels": {"com.docker.compose.service": service}},
                         "NetworkSettings": {"Ports": {"8443/tcp": [{"HostIp": "127.0.0.1", "HostPort": "8443"}]}}}
            with patch("deploy.socket.socket") as socket:
                socket.return_value.__enter__.return_value.bind.side_effect = OSError("occupied")
                if service == "backend":
                    with self.assertRaises(WorkflowError):
                        deploy.port_check([incumbent])
                else:
                    self.assertEqual(deploy.port_check([incumbent]), "owned-by-existing-release-nginx")

    def test_backup_failure_always_unpauses_incumbent(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            directory.chmod(0o700)
            incumbent = {"Id": "existing", "State": {"Running": True}}
            config = {"services": {"nginx": {"image": "approved@sha256:" + "a" * 64}},
                      "volumes": {"backend_state": {"name": "existing-volume"}}}
            def command(args):
                if args[1:3] == ["volume", "inspect"]:
                    raise WorkflowError("volume unavailable")
            with patch("deploy.recovery.recovery_key", return_value=b"x" * 32), patch("deploy.run", side_effect=command) as run:
                with self.assertRaises(WorkflowError):
                    deploy.snapshot(None, config, [incumbent], directory, directory / "key")
            self.assertEqual(run.call_args_list[-1], unittest.mock.call(["docker", "unpause", "existing"]))


if __name__ == "__main__":
    unittest.main()
