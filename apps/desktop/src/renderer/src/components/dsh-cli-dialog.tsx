import { useEffect, useState } from "react";
import { Button } from "@multica/ui/components/ui/button";
import { Terminal } from "lucide-react";
import {
  Dialog, DialogTrigger, DialogContent, DialogHeader,
  DialogTitle, DialogDescription, DialogFooter,
} from "@multica/ui/components/ui/dialog";
import { useT } from "@multica/views/i18n";
import type { DshCliStatus } from "../../../shared/dsh-cli";

function DshCliDialogBody() {
  const { t } = useT("settings");
  const [status, setStatus] = useState<DshCliStatus | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let active = true;
    window.daemonAPI.getDshCliStatus()
      .then((result) => { if (active) setStatus(result); })
      .catch(() => { if (active) setStatus({ state: "error" }); });
    return () => { active = false; };
  }, []);

  async function check() {
    setBusy(true);
    try {
      setStatus(await window.daemonAPI.getDshCliStatus());
    } catch {
      setStatus({ state: "error" });
    } finally { setBusy(false); }
  }

  const description = status === null ? t(($) => $.desktop.daemon.cli_checking)
    : status.state === "ready" ? t(($) => $.desktop.daemon.dsh_ready)
    : status.state === "not_installed" ? t(($) => $.desktop.daemon.dsh_not_installed)
    : status.reason === "probe_failed" ? t(($) => $.desktop.daemon.dsh_probe_failed)
    : status.reason === "probe_timeout" ? t(($) => $.desktop.daemon.dsh_probe_timeout)
    : status.reason === "protocol_incompatible" ? t(($) => $.desktop.daemon.dsh_protocol_incompatible)
    : status.reason === "launch_failed" ? t(($) => $.desktop.daemon.dsh_launch_failed)
    : t(($) => $.desktop.daemon.dsh_error);

  return (
    <>
      <DialogHeader>
        <DialogTitle>DSH CLI</DialogTitle>
        <DialogDescription aria-live="polite">{description}</DialogDescription>
      </DialogHeader>
      <DialogFooter aria-busy={busy}>
        <Button variant="outline" size="sm" disabled={busy || status === null} onClick={() => void check()}>
          {busy ? t(($) => $.desktop.daemon.cli_checking) : t(($) => $.desktop.daemon.dsh_check)}
        </Button>
      </DialogFooter>
    </>
  );
}

// 2026-10-08 coder(lq): The dialog checks on open; daemon startup prepares the official launcher independently.
export function DshCliAction() {
  return (
    <Dialog>
      <DialogTrigger render={<Button type="button" variant="outline" size="sm" />}>
        <Terminal aria-hidden="true" className="size-3.5" />
        DSH CLI
      </DialogTrigger>
      <DialogContent>
        <DshCliDialogBody />
      </DialogContent>
    </Dialog>
  );
}
