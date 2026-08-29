import { useEffect, useState } from "react";
import { RefreshCw, X } from "lucide-react";

// Downloads run silently in the background (main process has
// autoDownload=true). The renderer only renders UI once the package is fully
// downloaded and waiting for a restart.
type UpdateState =
  | { status: "idle" }
  | { status: "ready"; version: string }
  | { status: "error"; message: string };

export function UpdateNotification() {
  const [state, setState] = useState<UpdateState>({ status: "idle" });
  const [dismissed, setDismissed] = useState(false);
  const [installing, setInstalling] = useState(false);
  const [installError, setInstallError] = useState<string | null>(null);

  useEffect(() => {
    const cleanup = window.updater.onUpdateDownloaded((info) => {
      setState({ status: "ready", version: info.version });
      setDismissed(false);
      setInstalling(false);
      setInstallError(null);
    });
    const cleanupError = window.updater.onUpdateError((error) => {
      setState({ status: "error", message: error.message });
      setDismissed(false);
      setInstalling(false);
      setInstallError(null);
    });
    return () => {
      cleanup();
      cleanupError();
    };
  }, []);

  if (state.status === "idle") return null;
  if (dismissed) return null;

  return (
    <div className="fixed bottom-4 right-4 z-50 w-80 rounded-lg border border-border bg-background p-4 shadow-lg animate-in slide-in-from-bottom-2 fade-in duration-300">
      <button
        type="button"
        onClick={() => setDismissed(true)}
        className="absolute top-2 right-2 rounded-md p-1 text-muted-foreground hover:text-foreground transition-colors"
      >
        <X className="size-3.5" />
      </button>

      <div className="flex items-start gap-3">
        <div className="mt-0.5 rounded-md bg-success/10 p-1.5">
          <RefreshCw className="size-4 text-success" />
        </div>
        <div className="flex-1 min-w-0">
          {state.status === "error" ? (
            <>
              <p className="text-body font-medium">Multica Lechun Update failed</p>
              <p role="alert" className="text-caption text-destructive mt-1">
                Update failed: {state.message}
              </p>
            </>
          ) : (
            <>
              <p className="text-body font-medium">Multica Lechun Update ready</p>
              <p className="text-caption text-muted-foreground mt-0.5">
                v{state.version} will be applied on next launch.
              </p>
            </>
          )}
          {state.status === "ready" && installError && (
            <p role="alert" className="text-caption text-destructive mt-1">
              Update failed: {installError}
            </p>
          )}
          {state.status === "ready" && (
            <div className="mt-2 flex items-center gap-1.5">
              <button
                type="button"
                disabled={installing}
                onClick={async () => {
                  setInstalling(true);
                  setInstallError(null);
                  try {
                    const result = await window.updater.installUpdate();
                    if (result && !result.success) {
                      setInstalling(false);
                      setInstallError(result.error);
                    }
                  } catch (error) {
                    setInstalling(false);
                    setInstallError(
                      error instanceof Error ? error.message : String(error),
                    );
                  }
                }}
                className="inline-flex items-center rounded-md bg-primary px-3 py-1.5 text-caption font-medium text-primary-foreground hover:bg-primary/90 transition-colors"
              >
                {installing ? "Restarting…" : "Restart now"}
              </button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
