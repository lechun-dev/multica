import { useEffect, useState } from "react";
import { Button } from "@multica/ui/components/ui/button";
import { SettingsRow } from "@multica/views/settings";
import { useT } from "@multica/views/i18n";
import type { DshCliStatus } from "../../../shared/dsh-cli";

export function DshCliSettingsRow() {
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

  async function check(repair: boolean) {
    setBusy(true);
    try {
      setStatus(await (repair ? window.daemonAPI.repairDshCli() : window.daemonAPI.getDshCliStatus()));
    } catch {
      setStatus({ state: "error" });
    } finally { setBusy(false); }
  }

  const description = status === null ? t(($) => $.desktop.daemon.cli_checking)
    : status.state === "ready" ? t(($) => $.desktop.daemon.dsh_ready)
    : status.state === "needs_repair" ? t(($) => $.desktop.daemon.dsh_needs_repair)
    : status.state === "not_installed" ? t(($) => $.desktop.daemon.dsh_not_installed)
    : status.state === "unsupported" ? t(($) => $.desktop.daemon.dsh_unsupported)
    : t(($) => $.desktop.daemon.dsh_error);

  return (
    <SettingsRow label="DSH CLI" description={description}>
      <div className="flex shrink-0 flex-wrap justify-end gap-2" aria-busy={busy}>
        {status?.state === "needs_repair" && (
          <Button variant="outline" size="sm" disabled={busy} onClick={() => void check(true)}>
            {t(($) => $.desktop.daemon.dsh_repair)}
          </Button>
        )}
        <Button variant="outline" size="sm" disabled={busy || status === null} onClick={() => void check(false)}>
          {busy ? t(($) => $.desktop.daemon.cli_checking) : t(($) => $.desktop.daemon.dsh_check)}
        </Button>
      </div>
    </SettingsRow>
  );
}
