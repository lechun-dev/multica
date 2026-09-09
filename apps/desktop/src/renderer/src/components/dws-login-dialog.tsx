import { useEffect, useState } from "react";
import { Link2, LoaderCircle, ShieldAlert } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { useT } from "@multica/views/i18n";
import type { DwsAuthRequirement } from "../../../shared/dws-auth";

type LoginPhase = "idle" | "authorizing" | "error";

export function DwsLoginDialog() {
  const { t } = useT("common");
  const [requirement, setRequirement] =
    useState<DwsAuthRequirement | null>(null);
  const [phase, setPhase] = useState<LoginPhase>("idle");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let active = true;
    void window.dwsAPI.getAuthRequirement().then((pending) => {
      if (active && pending) setRequirement(pending);
    });
    const stopRequired = window.dwsAPI.onAuthRequired((next) => {
      setRequirement(next);
      setPhase("idle");
      setError(null);
    });
    const stopResolved = window.dwsAPI.onAuthResolved(() => {
      setRequirement(null);
      setPhase("idle");
      setError(null);
    });
    return () => {
      active = false;
      stopRequired();
      stopResolved();
    };
  }, []);

  const authorizing = phase === "authorizing";
  const title =
    requirement?.reason === "not_installed"
      ? t(($) => $.desktop.dws_auth.not_installed_title)
      : requirement?.reason === "identity_mismatch"
        ? t(($) => $.desktop.dws_auth.identity_mismatch_title)
        : t(($) => $.desktop.dws_auth.not_logged_in_title);
  const description =
    requirement?.reason === "not_installed"
      ? t(($) => $.desktop.dws_auth.not_installed)
      : requirement?.reason === "identity_mismatch"
        ? t(($) => $.desktop.dws_auth.identity_mismatch)
        : t(($) => $.desktop.dws_auth.description);
  const actionLabel =
    requirement?.reason === "not_installed"
      ? t(($) => $.desktop.dws_auth.detect_and_login)
      : requirement?.reason === "identity_mismatch"
        ? t(($) => $.desktop.dws_auth.reauthorize)
        : t(($) => $.desktop.dws_auth.login);

  const handleLogin = async () => {
    setPhase("authorizing");
    setError(null);
    try {
      const result = await window.dwsAPI.login();
      if (!result.ok) {
        setPhase("error");
        setError(result.message || t(($) => $.desktop.dws_auth.failed));
        return;
      }
      toast.success(t(($) => $.desktop.dws_auth.success));
      setRequirement(null);
      setPhase("idle");
    } catch (loginError) {
      setPhase("error");
      setError(
        loginError instanceof Error && loginError.message
          ? loginError.message
          : t(($) => $.desktop.dws_auth.failed),
      );
    }
  };

  return (
    <Dialog
      open={requirement !== null}
      onOpenChange={(open) => {
        if (!open && !authorizing) {
          setRequirement(null);
          setPhase("idle");
          setError(null);
        }
      }}
    >
      <DialogContent showCloseButton={!authorizing} className="sm:max-w-md">
        <DialogHeader>
          <div className="flex items-start gap-3 pr-6">
            <div className="rounded-lg bg-brand/10 p-2 text-brand">
              {requirement?.reason === "identity_mismatch" ? (
                <ShieldAlert className="size-5" />
              ) : (
                <Link2 className="size-5" />
              )}
            </div>
            <div className="min-w-0 space-y-2">
              <DialogTitle>{title}</DialogTitle>
              <DialogDescription>{description}</DialogDescription>
            </div>
          </div>
        </DialogHeader>

        {requirement?.reason !== "not_installed" && (
          <p className="rounded-lg bg-surface-hover px-3 py-2 text-caption text-muted-foreground">
            {t(($) => $.desktop.dws_auth.browser_hint)}
          </p>
        )}
        {authorizing && (
          <div className="flex items-center gap-2 text-body text-muted-foreground">
            <LoaderCircle className="size-4 animate-spin" />
            <span>{t(($) => $.desktop.dws_auth.authorizing)}</span>
          </div>
        )}
        {error && (
          <p role="alert" className="text-body text-destructive">
            {error}
          </p>
        )}

        <DialogFooter>
          <Button
            variant="outline"
            disabled={authorizing}
            onClick={() => setRequirement(null)}
          >
            {t(($) => $.desktop.dws_auth.later)}
          </Button>
          <Button disabled={authorizing} onClick={() => void handleLogin()}>
            {authorizing && <LoaderCircle className="animate-spin" />}
            {actionLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
