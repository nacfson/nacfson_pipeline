import pathlib
import subprocess
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
SKILL_PATH = ROOT / ".agents" / "skills" / "debug-mcp-session" / "SKILL.md"
SESSION_SCRIPT = ROOT / "scripts" / "mcp" / "mcp-session.sh"
REFRESH_SCRIPT = ROOT / "scripts" / "mcp" / "refresh-kubeconfig.sh"
INSTALL_SCRIPT = ROOT / "scripts" / "mcp" / "install-daemon.sh"
SERVICE_UNIT = ROOT / "infra" / "workstation" / "nacfson-mcp.service"


class TestDebugMcpSessionSkill(unittest.TestCase):
    def test_skill_frontmatter_and_required_references(self) -> None:
        self.assertTrue(SKILL_PATH.is_file(), f"Missing {SKILL_PATH}")
        text = SKILL_PATH.read_text(encoding="utf-8")
        self.assertTrue(text.startswith("---\n"))
        self.assertIn("name: debug-mcp-session", text)
        self.assertIn("scripts/mcp/mcp-session.sh", text)
        self.assertIn("scripts/mcp/install-daemon.sh", text)
        self.assertIn("RuntimeMaxSec", text)
        self.assertIn("--connect", text)
        self.assertIn("ExecStopPost", text)
        self.assertIn("10m..24h", text)

    def test_systemd_unit_and_installer_default_to_ephemeral_mode(self) -> None:
        unit_text = SERVICE_UNIT.read_text(encoding="utf-8")
        self.assertIn("Restart=no", unit_text)
        self.assertIn("RuntimeMaxSec=2h", unit_text)
        self.assertIn("ReadWritePaths=/etc/nacfson-mcp", unit_text)
        self.assertIn("ExecStopPost=+/bin/rm -f /etc/nacfson-mcp/kubeconfig", unit_text)

        install_text = INSTALL_SCRIPT.read_text(encoding="utf-8")
        self.assertIn("ENABLE_ON_BOOT=0", install_text)
        self.assertIn("--enable", install_text)
        self.assertIn("systemctl disable nacfson-mcp.service", install_text)
        self.assertIn("mcp-session.sh start", install_text)

    def test_session_script_status_and_ttl_validation(self) -> None:
        status_proc = subprocess.run(
            ["bash", str(SESSION_SCRIPT), "status"],
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(status_proc.returncode, 0, status_proc.stderr)
        self.assertIn("binary: /usr/local/bin/nacfson-mcp", status_proc.stdout)
        self.assertIn("mcp-session.sh start", status_proc.stdout)

        for bad_ttl in ("0h", "1m", "5m", "9m", "25h", "1441m", "abc", "-1h", "1h\nExecStart=/bin/sh"):
            proc = subprocess.run(
                ["bash", str(SESSION_SCRIPT), "start", "gke", "--ttl", bad_ttl],
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 2, f"expected exit 2 for ttl={bad_ttl!r}, got {proc.returncode}")

    def test_refresh_rbac_fail_closed_and_arg_validation(self) -> None:
        refresh_text = REFRESH_SCRIPT.read_text(encoding="utf-8")
        self.assertIn("for verb in get list watch", refresh_text)
        self.assertIn('"$CAN_VERB" != "no"', refresh_text)
        self.assertIn('"$res" != "no"', refresh_text)
        self.assertIn("neither kubectl nor k3s is installed locally", refresh_text)
        self.assertIn("if [ $# -lt 2 ]; then", refresh_text)

        install_text = INSTALL_SCRIPT.read_text(encoding="utf-8")
        self.assertIn("if [ $# -lt 2 ]; then", install_text)


if __name__ == "__main__":
    unittest.main()
