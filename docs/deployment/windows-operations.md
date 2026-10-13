# Windows operator helper status

The supported Ticket 12 workflow for this local release scope runs from
WSL/Linux. Use [`local-operations.md`](local-operations.md) and
[`scripts/local_operations.py`](../../scripts/local_operations.py) for
preflight, start, status, stop, update, rollback, and diagnostics.

[`scripts/windows-operations.ps1`](../../scripts/windows-operations.ps1) is
retained as a compatibility helper. Windows PowerShell, Docker Desktop restart,
and NTFS checks are optional and are reported as `not_run` by the current local
workflow. The prior Windows-specific run remains as historical evidence in
[`ticket-12-windows-evidence.md`](ticket-12-windows-evidence.md); it does not
replace the current WSL/Linux acceptance scope.
